(function () {
  const serverId = window.SERVER_ID;
  const box = document.getElementById("console-box");
  const form = document.getElementById("console-form");
  const input = document.getElementById("console-input");

  function appendLine(text) {
    const atBottom = box.scrollTop + box.clientHeight >= box.scrollHeight - 20;
    const line = document.createElement("div");
    line.textContent = text;
    box.appendChild(line);
    while (box.childElementCount > 2000) {
      box.removeChild(box.firstChild);
    }
    if (atBottom) box.scrollTop = box.scrollHeight;
  }

  function connect() {
    const source = new EventSource(`/server/${serverId}/console/stream`);
    source.onmessage = (event) => appendLine(event.data);
    source.onerror = () => {
      source.close();
      appendLine("[console disconnected, reconnecting in 3s...]");
      setTimeout(connect, 3000);
    };
  }
  connect();

  form.addEventListener("submit", async (event) => {
    event.preventDefault();
    const command = input.value;
    if (!command.trim()) return;
    input.value = "";
    try {
      const resp = await fetch(`/server/${serverId}/console/send`, {
        method: "POST",
        headers: { "Content-Type": "application/x-www-form-urlencoded" },
        body: new URLSearchParams({ command, csrf_token: window.CSRF_TOKEN }),
      });
      const data = await resp.json();
      if (!data.ok) appendLine(`[error sending command: ${data.error}]`);
    } catch (err) {
      appendLine(`[error sending command: ${err}]`);
    }
  });

  async function pollStats() {
    try {
      const resp = await fetch(`/server/${serverId}/stats`);
      const data = await resp.json();
      const cpuEl = document.getElementById("stat-cpu");
      const memEl = document.getElementById("stat-mem");
      if (data.status === "unavailable" || data.cpu_percent === undefined) {
        cpuEl.textContent = "–";
        memEl.textContent = "–";
      } else {
        cpuEl.textContent = `${data.cpu_percent}%`;
        memEl.textContent = `${data.mem_usage_mb} / ${data.mem_limit_mb} MB`;
      }
    } catch (err) {
      // ignore transient errors
    }
  }
  pollStats();
  setInterval(pollStats, 3000);
})();
