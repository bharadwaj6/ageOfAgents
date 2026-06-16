package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"strings"
)

func cmdServe(args []string) error {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	port := fs.Int("port", 8080, "Port to listen on")
	path := fs.String("path", ".", "Workspace root directory")
	secret := fs.String("secret", "", "GitHub webhook secret")
	fs.Parse(args)

	ws, err := workspaceAt(*path)
	if err != nil {
		return err
	}

	http.HandleFunc("/webhook", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, "Error reading body", http.StatusBadRequest)
			return
		}

		if *secret != "" {
			sig := r.Header.Get("X-Hub-Signature-256")
			if !verifySignature(sig, body, []byte(*secret)) {
				http.Error(w, "Invalid signature", http.StatusUnauthorized)
				return
			}
		}

		event := r.Header.Get("X-GitHub-Event")
		if event == "issue_comment" {
			var payload struct {
				Action string `json:"action"`
				Issue  struct {
					Number int `json:"number"`
				} `json:"issue"`
				Comment struct {
					Body string `json:"body"`
				} `json:"comment"`
			}
			if err := json.Unmarshal(body, &payload); err == nil {
				if payload.Action == "created" && strings.HasPrefix(strings.TrimSpace(payload.Comment.Body), "@aoa ") {
					cmd := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(payload.Comment.Body), "@aoa"))
					if cmd != "" {
						// Spawn aoa goal and aoa run in background
						go func() {
							_ = runGoal(ws, cmd)
							_ = cmdRun([]string{"--path", ws.root, "--once"})
						}()
					}
				}
			}
		}

		w.WriteHeader(http.StatusOK)
		fmt.Fprintln(w, "OK")
	})

	fmt.Printf("Age of Agents webhook server listening on :%d\n", *port)
	return http.ListenAndServe(fmt.Sprintf(":%d", *port), nil)
}

func verifySignature(signature string, payload, secret []byte) bool {
	if !strings.HasPrefix(signature, "sha256=") {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write(payload)
	expectedMAC := hex.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(signature[7:]), []byte(expectedMAC))
}

func runGoal(ws workspace, text string) error {
	// Simple wrapper to run goal logic
	args := []string{"--path", ws.root, text}
	return cmdGoal(args)
}
