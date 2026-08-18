// Package dockermgr wraps the Docker SDK for everything the panel needs:
// create/start/stop/restart/kill/remove containers, pull stats, tail logs,
// and write commands to a container's stdin (for game server consoles).
//
// NOTE: whatever process runs this app needs access to the Docker socket
// (usually membership in the docker group, or root). That is effectively
// root-equivalent power on this host, so this app's own account/session
// security matters a lot -- see README.md.
package dockermgr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"regexp"
	"strconv"
	"strings"
	"sync"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
)

type Manager struct {
	cli          *client.Client
	once         sync.Once
	initErr      error
	attachMu     sync.Mutex
	attachConns  map[string]net.Conn
	attachWriter map[string]*bufio.Writer
}

func New() *Manager {
	return &Manager{
		attachConns:  make(map[string]net.Conn),
		attachWriter: make(map[string]*bufio.Writer),
	}
}

func (m *Manager) client() (*client.Client, error) {
	m.once.Do(func() {
		m.cli, m.initErr = client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	})
	return m.cli, m.initErr
}

var slugStrip = regexp.MustCompile(`[^a-zA-Z0-9_-]+`)

func Slugify(name string) string {
	slug := slugStrip.ReplaceAllString(strings.ToLower(strings.TrimSpace(name)), "-")
	slug = strings.Trim(slug, "-")
	if slug == "" {
		return "server"
	}
	return slug
}

var portMappingRe = regexp.MustCompile(`^(\d{1,5}):(\d{1,5})(/(tcp|udp))?$`)

// ParsePortMappings parses newline/comma separated "hostport:containerport/proto"
// entries into a nat.PortMap, e.g. "25565:25565/tcp".
func ParsePortMappings(raw string) (nat.PortMap, error) {
	ports := nat.PortMap{}
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ports, nil
	}
	entries := splitCommaNewline(raw)
	for _, entry := range entries {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		match := portMappingRe.FindStringSubmatch(entry)
		if match == nil {
			return nil, fmt.Errorf("invalid port mapping: %q (expected hostport:containerport[/tcp|udp])", entry)
		}
		hostPort, containerPort, proto := match[1], match[2], match[4]
		if proto == "" {
			proto = "tcp"
		}
		for _, p := range []string{hostPort, containerPort} {
			n, _ := strconv.Atoi(p)
			if n <= 0 || n >= 65536 {
				return nil, fmt.Errorf("port out of range in mapping: %q", entry)
			}
		}
		key := nat.Port(containerPort + "/" + proto)
		ports[key] = append(ports[key], nat.PortBinding{HostPort: hostPort})
	}
	return ports, nil
}

var envKeyRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ParseEnv parses newline separated KEY=VALUE lines.
func ParseEnv(raw string) (map[string]string, error) {
	env := map[string]string{}
	if strings.TrimSpace(raw) == "" {
		return env, nil
	}
	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		idx := strings.Index(line, "=")
		if idx < 0 {
			return nil, fmt.Errorf("invalid env line (expected KEY=VALUE): %q", line)
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if !envKeyRe.MatchString(key) {
			return nil, fmt.Errorf("invalid env var name: %q", key)
		}
		env[key] = value
	}
	return env, nil
}

func splitCommaNewline(raw string) []string {
	replaced := strings.NewReplacer("\r\n", "\n", "\r", "\n").Replace(raw)
	var out []string
	for _, part := range strings.Split(replaced, "\n") {
		out = append(out, strings.Split(part, ",")...)
	}
	return out
}

func (m *Manager) CreateContainer(ctx context.Context, containerName, image, dataPath string, memoryMB int, ports nat.PortMap, env map[string]string) (string, error) {
	return m.createContainer(ctx, containerName, image, dataPath, memoryMB, ports, env, nil)
}

// CreateContainerWithCommand is like CreateContainer but overrides the
// container's command (used for the lightweight demo/screenshot containers,
// which run a fake console-output loop instead of a real game server image).
func (m *Manager) CreateContainerWithCommand(ctx context.Context, containerName, image, dataPath string, memoryMB int, cmd []string) (string, error) {
	return m.createContainer(ctx, containerName, image, dataPath, memoryMB, nil, nil, cmd)
}

func (m *Manager) createContainer(ctx context.Context, containerName, image, dataPath string, memoryMB int, ports nat.PortMap, env map[string]string, cmd []string) (string, error) {
	cli, err := m.client()
	if err != nil {
		return "", err
	}

	// docker-py's containers.run() (used by the old Python implementation)
	// auto-pulls the image if it isn't present locally; the raw engine API
	// (ContainerCreate) does not, so replicate that convenience here.
	if _, _, err := cli.ImageInspectWithRaw(ctx, image); err != nil {
		if pullErr := m.PullImage(ctx, image); pullErr != nil {
			return "", fmt.Errorf("image %q not found locally and pull failed: %w", image, pullErr)
		}
	}

	envList := make([]string, 0, len(env))
	for k, v := range env {
		envList = append(envList, k+"="+v)
	}

	exposed := nat.PortSet{}
	for p := range ports {
		exposed[p] = struct{}{}
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image:        image,
		Cmd:          cmd,
		Env:          envList,
		Tty:          true,
		OpenStdin:    true,
		StdinOnce:    false,
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
		ExposedPorts: exposed,
	}, &container.HostConfig{
		Mounts: []mount.Mount{{
			Type:   mount.TypeBind,
			Source: dataPath,
			Target: "/data",
		}},
		PortBindings: ports,
		Resources: container.Resources{
			Memory: int64(memoryMB) * 1024 * 1024,
		},
		RestartPolicy: container.RestartPolicy{Name: "unless-stopped"},
	}, &network.NetworkingConfig{}, nil, containerName)
	if err != nil {
		return "", err
	}
	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return "", err
	}
	return resp.ID, nil
}

func (m *Manager) ContainerStatus(ctx context.Context, containerID string) string {
	cli, err := m.client()
	if err != nil {
		return "missing"
	}
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return "missing"
	}
	return info.State.Status
}

// PullImage pulls image (e.g. "alpine:latest") and drains/discards the
// progress stream, only returning once the pull has finished or failed.
func (m *Manager) PullImage(ctx context.Context, image string) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	reader, err := cli.ImagePull(ctx, image, types.ImagePullOptions{})
	if err != nil {
		return err
	}
	defer reader.Close()
	_, err = io.Copy(io.Discard, reader)
	return err
}

func (m *Manager) PowerAction(ctx context.Context, containerID, action string) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	timeout := 30
	switch action {
	case "start":
		return cli.ContainerStart(ctx, containerID, types.ContainerStartOptions{})
	case "stop":
		return cli.ContainerStop(ctx, containerID, container.StopOptions{Timeout: &timeout})
	case "restart":
		return cli.ContainerRestart(ctx, containerID, container.StopOptions{Timeout: &timeout})
	case "kill":
		return cli.ContainerKill(ctx, containerID, "KILL")
	default:
		return fmt.Errorf("unknown power action: %s", action)
	}
}

func (m *Manager) RemoveContainer(ctx context.Context, containerID string) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	err = cli.ContainerRemove(ctx, containerID, types.ContainerRemoveOptions{Force: true})
	if client.IsErrNotFound(err) {
		return nil
	}
	return err
}

// StreamLogs follows the container's combined stdout/stderr, writing decoded
// lines to the callback until the context is cancelled or the stream ends.
func (m *Manager) StreamLogs(ctx context.Context, containerID string, onLine func(string)) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	tail := "200"
	reader, err := cli.ContainerLogs(ctx, containerID, types.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Follow:     true,
		Tail:       tail,
	})
	if err != nil {
		return err
	}
	defer reader.Close()

	scanner := bufio.NewScanner(reader)
	scanner.Buffer(make([]byte, 64*1024), 1024*1024)
	for scanner.Scan() {
		onLine(scanner.Text())
	}
	return scanner.Err()
}

type Stats struct {
	Status     string
	CPUPercent float64
	MemUsageMB float64
	MemLimitMB float64
	MemPercent float64
	NetRxMB    float64
	NetTxMB    float64
}

func (m *Manager) GetStats(ctx context.Context, containerID string) (*Stats, error) {
	cli, err := m.client()
	if err != nil {
		return nil, err
	}
	info, err := cli.ContainerInspect(ctx, containerID)
	if err != nil {
		return nil, err
	}
	resp, err := cli.ContainerStatsOneShot(ctx, containerID)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var raw types.StatsJSON
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	var cpuPercent float64
	cpuDelta := float64(raw.CPUStats.CPUUsage.TotalUsage) - float64(raw.PreCPUStats.CPUUsage.TotalUsage)
	systemDelta := float64(raw.CPUStats.SystemUsage) - float64(raw.PreCPUStats.SystemUsage)
	onlineCPUs := float64(raw.CPUStats.OnlineCPUs)
	if onlineCPUs == 0 {
		onlineCPUs = float64(len(raw.CPUStats.CPUUsage.PercpuUsage))
	}
	if onlineCPUs == 0 {
		onlineCPUs = 1
	}
	if systemDelta > 0 && cpuDelta >= 0 {
		cpuPercent = (cpuDelta / systemDelta) * onlineCPUs * 100.0
	}

	memUsage := float64(raw.MemoryStats.Usage)
	memLimit := float64(raw.MemoryStats.Limit)
	cache := float64(raw.MemoryStats.Stats["cache"])
	memUsageAdjusted := memUsage - cache
	if memUsageAdjusted < 0 {
		memUsageAdjusted = 0
	}

	var netRx, netTx float64
	for _, iface := range raw.Networks {
		netRx += float64(iface.RxBytes)
		netTx += float64(iface.TxBytes)
	}

	var memPercent float64
	if memLimit > 0 {
		memPercent = round1((memUsageAdjusted / memLimit) * 100)
	}

	return &Stats{
		Status:     info.State.Status,
		CPUPercent: round1(cpuPercent),
		MemUsageMB: round1(memUsageAdjusted / (1024 * 1024)),
		MemLimitMB: round1(memLimit / (1024 * 1024)),
		MemPercent: memPercent,
		NetRxMB:    round2(netRx / (1024 * 1024)),
		NetTxMB:    round2(netTx / (1024 * 1024)),
	}, nil
}

func round1(f float64) float64 {
	return float64(int(f*10+0.5)) / 10
}

func round2(f float64) float64 {
	return float64(int(f*100+0.5)) / 100
}

// SendCommand writes a line to the container's stdin over a persistent attach
// connection, opening one on first use and reopening it if the write fails.
func (m *Manager) SendCommand(ctx context.Context, containerID, command string) error {
	cli, err := m.client()
	if err != nil {
		return err
	}

	m.attachMu.Lock()
	writer, ok := m.attachWriter[containerID]
	if !ok {
		hijacked, err := cli.ContainerAttach(ctx, containerID, types.ContainerAttachOptions{
			Stream: true,
			Stdin:  true,
		})
		if err != nil {
			m.attachMu.Unlock()
			return err
		}
		m.attachConns[containerID] = hijacked.Conn
		writer = bufio.NewWriter(hijacked.Conn)
		m.attachWriter[containerID] = writer
	}
	m.attachMu.Unlock()

	data := strings.TrimRight(command, "\n") + "\n"
	if _, err := writer.WriteString(data); err != nil {
		m.dropAttachLocked(containerID)
		return fmt.Errorf("failed to send command, console link reset -- try again: %w", err)
	}
	if err := writer.Flush(); err != nil {
		m.dropAttachLocked(containerID)
		return fmt.Errorf("failed to send command, console link reset -- try again: %w", err)
	}
	return nil
}

func (m *Manager) dropAttachLocked(containerID string) {
	m.attachMu.Lock()
	defer m.attachMu.Unlock()
	if conn, ok := m.attachConns[containerID]; ok {
		conn.Close()
	}
	delete(m.attachConns, containerID)
	delete(m.attachWriter, containerID)
}

func (m *Manager) DropAttachSocket(containerID string) {
	m.dropAttachLocked(containerID)
}

// ExecRemovePaths deletes relPaths (each relative to /data, the container's
// game-data mount) from inside the container as root via docker exec.
// Needed as a fallback when host-side deletion of bind-mounted game data
// fails with a permission error -- game images commonly write world files as
// their own internal UID (itzg's images default to uid 1000), which the
// host user running this panel often can't delete directly. Exec'ing into
// the container's own namespace sidesteps that, since docker exec runs as
// root there regardless of the panel's host UID. The container must be
// running.
func (m *Manager) ExecRemovePaths(ctx context.Context, containerID string, relPaths []string) error {
	if len(relPaths) == 0 {
		return nil
	}
	cli, err := m.client()
	if err != nil {
		return err
	}
	cmd := []string{"rm", "-rf"}
	for _, p := range relPaths {
		cmd = append(cmd, "/data/"+p)
	}
	execID, err := cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		Cmd:          cmd,
		User:         "0",
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attachResp, err := cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()
	output, _ := io.ReadAll(attachResp.Reader)
	inspect, err := cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("rm inside container exited %d: %s", inspect.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}

// ExecWriteFile streams r's contents to destPath (an absolute in-container
// path, e.g. "/data/plugins/foo.jar.tmp") as root via docker exec + tee.
// Fallback for the same host/container UID mismatch ExecRemovePaths exists
// for: itzg's images create files as their own internal uid, which the host
// panel process often can't write into directly. Uses `tee <path>` rather
// than a shell redirect so destPath is passed as a literal argv element --
// no shell involved, so nothing in it needs quoting/escaping regardless of
// its content. The container must be running.
func (m *Manager) ExecWriteFile(ctx context.Context, containerID, destPath string, r io.Reader) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	execID, err := cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		Cmd:          []string{"tee", destPath},
		User:         "0",
		AttachStdin:  true,
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attachResp, err := cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()

	_, copyErr := io.Copy(attachResp.Conn, r)
	// Half-close so `tee` sees EOF on stdin and exits, without tearing down
	// the connection we still need to read stdout/stderr from below.
	if cw, ok := attachResp.Conn.(interface{ CloseWrite() error }); ok {
		_ = cw.CloseWrite()
	} else {
		_ = attachResp.Conn.Close()
	}
	if copyErr != nil {
		return fmt.Errorf("write to exec stdin: %w", copyErr)
	}

	output, _ := io.ReadAll(attachResp.Reader)
	inspect, err := cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("tee inside container exited %d: %s", inspect.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}

// ExecRename renames oldPath to newPath (both absolute in-container paths)
// as root via docker exec -- the atomic-rename half of the ExecWriteFile
// fallback, so a plugin jar copied through this path is still written to a
// ".tmp" sibling and renamed into place rather than appearing incrementally
// at its final name. The container must be running.
func (m *Manager) ExecRename(ctx context.Context, containerID, oldPath, newPath string) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	execID, err := cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		Cmd:          []string{"mv", oldPath, newPath},
		User:         "0",
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attachResp, err := cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()
	output, _ := io.ReadAll(attachResp.Reader)
	inspect, err := cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("mv inside container exited %d: %s", inspect.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}

// ExecMkdir creates dir (an absolute in-container path) as root via docker
// exec, matching mkdir -p semantics (no error if it already exists). Used
// before ExecWriteFile so the plugins/ fallback works even against a
// container that's never had a plugin installed and so has no plugins/
// directory of its own yet. The container must be running.
func (m *Manager) ExecMkdir(ctx context.Context, containerID, dir string) error {
	cli, err := m.client()
	if err != nil {
		return err
	}
	execID, err := cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		Cmd:          []string{"mkdir", "-p", dir},
		User:         "0",
		AttachStdout: true,
		AttachStderr: true,
	})
	if err != nil {
		return fmt.Errorf("exec create: %w", err)
	}
	attachResp, err := cli.ContainerExecAttach(ctx, execID.ID, types.ExecStartCheck{})
	if err != nil {
		return fmt.Errorf("exec attach: %w", err)
	}
	defer attachResp.Close()
	output, _ := io.ReadAll(attachResp.Reader)
	inspect, err := cli.ContainerExecInspect(ctx, execID.ID)
	if err != nil {
		return fmt.Errorf("exec inspect: %w", err)
	}
	if inspect.ExitCode != 0 {
		return fmt.Errorf("mkdir inside container exited %d: %s", inspect.ExitCode, strings.TrimSpace(string(output)))
	}
	return nil
}
