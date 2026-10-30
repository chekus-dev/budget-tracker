package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

// categoryStyleHandler tells the Settings page which icon and colour a typed
// category name gets, so a new row updates as the user types.
func categoryStyleHandler(w http.ResponseWriter, r *http.Request) {
	name := strings.TrimSpace(r.URL.Query().Get("name"))
	if rs := []rune(name); len(rs) > 50 {
		name = string(rs[:50])
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=300")
	json.NewEncoder(w).Encode(map[string]string{"icon": catIcon(name), "color": catColor(name)})
}
