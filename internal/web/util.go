package web

import "strconv"

func itoa(id int64) string {
	return strconv.FormatInt(id, 10)
}
