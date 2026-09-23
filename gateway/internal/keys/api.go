package keys

// Dashboard API:
//
//	GET  /api/keys                 every key (never the key itself)
//	POST /api/keys                 {name, aliases?, routes?, rpm?, tokens_per_day?} -> {key, view}; key is shown once
//	PUT  /api/keys/{id}            {name?, aliases, routes, rpm, tokens_per_day}
//	POST /api/keys/{id}/revoke     revoke for good

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
)

// Register mounts the keys API.
func (s *Store) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /api/keys", s.list)
	mux.HandleFunc("POST /api/keys", s.create)
	mux.HandleFunc("PUT /api/keys/{id}", s.update)
	mux.HandleFunc("POST /api/keys/{id}/revoke", s.revoke)
}

type keyInput struct {
	Name string `json:"name"`
	Limits
}

func (s *Store) list(w http.ResponseWriter, _ *http.Request) { writeJSON(w, http.StatusOK, s.List()) }

// Created is the one response that carries a key in plaintext.
type Created struct {
	Key  string `json:"key"`
	View View   `json:"view"`
	Note string `json:"note"`
}

func (s *Store) create(w http.ResponseWriter, r *http.Request) {
	var in keyInput
	if !readJSON(w, r, &in) {
		return
	}
	key, v, err := s.Create(in.Name, in.Limits)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusCreated, Created{Key: key, View: v,
		Note: "Copy the key now: the gateway only keeps its hash and cannot show it again."})
}

func (s *Store) update(w http.ResponseWriter, r *http.Request) {
	var in keyInput
	if !readJSON(w, r, &in) {
		return
	}
	v, err := s.Update(r.PathValue("id"), in.Name, in.Limits)
	respond(w, v, err)
}

func (s *Store) revoke(w http.ResponseWriter, r *http.Request) {
	v, err := s.Revoke(r.PathValue("id"))
	respond(w, v, err)
}

func respond(w http.ResponseWriter, v View, err error) {
	switch {
	case errors.Is(err, os.ErrNotExist):
		writeError(w, http.StatusNotFound, "no such key")
	case err != nil:
		writeError(w, http.StatusBadRequest, err.Error())
	default:
		writeJSON(w, http.StatusOK, v)
	}
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<16))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return false
	}
	return true
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}
