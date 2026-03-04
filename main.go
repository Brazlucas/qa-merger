package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

//go:embed static/*
var staticFiles embed.FS

func runGitCommand(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.String(), err
}

func main() {
	staticFS, err := fs.Sub(staticFiles, "static")
	if err != nil {
		log.Fatal(err)
	}

	http.Handle("/", http.FileServer(http.FS(staticFS)))

	http.HandleFunc("/api/browse", handleBrowse)
	http.HandleFunc("/api/branches", handleBranches)
	http.HandleFunc("/api/merge", handleMerge)

	port := ":8080"
	url := "http://localhost" + port
	fmt.Printf("Servidor QA Merger Web UI iniciado!\nNavegue para %s\n", url)

	go func() {
		time.Sleep(500 * time.Millisecond) // Give server time to start
		openBrowser(url)
	}()

	log.Fatal(http.ListenAndServe(port, nil))
}

func openBrowser(url string) {
	var err error
	switch runtime.GOOS {
	case "linux":
		err = exec.Command("xdg-open", url).Start()
	case "windows":
		err = exec.Command("rundll32", "url.dll,FileProtocolHandler", url).Start()
	case "darwin":
		err = exec.Command("open", url).Start()
	default:
		err = fmt.Errorf("unsupported platform")
	}
	if err != nil {
		log.Printf("Erro ao abrir navegador: %v", err)
	}
}

func isWSL() bool {
	if runtime.GOOS == "linux" {
		out, err := exec.Command("uname", "-r").Output()
		if err == nil {
			lowerOut := strings.ToLower(string(out))
			if strings.Contains(lowerOut, "microsoft") || strings.Contains(lowerOut, "wsl") {
				return true
			}
		}
	}
	return false
}

func handleBrowse(w http.ResponseWriter, r *http.Request) {
	var cmd *exec.Cmd
	var isPowershellWSL bool

	if isWSL() {
		psScript := `Add-Type -AssemblyName System.windows.forms; $f=New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description="Selecione o Projeto Front-End"; if ($f.ShowDialog() -eq 'OK') { Write-Output $f.SelectedPath }`
		cmd = exec.Command("powershell.exe", "-NoProfile", "-Command", psScript)
		isPowershellWSL = true
	} else {
		switch runtime.GOOS {
		case "linux":
			if _, err := exec.LookPath("zenity"); err == nil {
				cmd = exec.Command("zenity", "--file-selection", "--directory", "--title=Selecione o Projeto Front-End")
			} else if _, err := exec.LookPath("kdialog"); err == nil {
				cmd = exec.Command("kdialog", "--getexistingdirectory", "/")
			} else {
				json.NewEncoder(w).Encode(map[string]interface{}{"error": "Requer zenity ou kdialog para abrir o explorador no Linux"})
				return
			}
		case "windows":
			psScript := `Add-Type -AssemblyName System.windows.forms; $f=New-Object System.Windows.Forms.FolderBrowserDialog; $f.Description="Selecione o Projeto Front-End"; if ($f.ShowDialog() -eq 'OK') { Write-Output $f.SelectedPath }`
			cmd = exec.Command("powershell", "-NoProfile", "-Command", psScript)
		case "darwin":
			cmd = exec.Command("osascript", "-e", `tell application "System Events" to activate`, "-e", `tell application "System Events" to return POSIX path of (choose folder with prompt "Selecione o Projeto Front-End")`)
		default:
			json.NewEncoder(w).Encode(map[string]interface{}{"error": "Sistema operacional não suportado"})
			return
		}
	}

	out, err := cmd.Output()
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "Seleção cancelada ou falhou"})
		return
	}

	path := strings.TrimSpace(string(out))
	if path == "" {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "Nenhum diretório selecionado"})
		return
	}

	if isPowershellWSL {
		pathBytes, err := exec.Command("wslpath", "-u", path).Output()
		if err == nil {
			path = strings.TrimSpace(string(pathBytes))
		}
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"path": path})
}

func handleBranches(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Query().Get("path")
	if path == "" {
		json.NewEncoder(w).Encode(map[string]string{"error": "Path is required"})
		return
	}

	_, err := runGitCommand(path, "fetch", "--all", "--prune")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "Erro no fetch: " + err.Error()})
		return
	}

	out, err := runGitCommand(path, "branch", "-r", "--format=%(refname:short)")
	if err != nil {
		json.NewEncoder(w).Encode(map[string]interface{}{"error": "Erro ao listar branches: " + err.Error()})
		return
	}

	lines := strings.Split(strings.TrimSpace(out), "\n")
	var branches []string
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || strings.Contains(line, "->") || strings.HasSuffix(line, "/HEAD") {
			continue
		}
		if strings.HasSuffix(line, "/quality-assurance") {
			continue
		}
		branches = append(branches, line)
	}

	json.NewEncoder(w).Encode(map[string]interface{}{"branches": branches})
}

type MergeRequest struct {
	Path         string `json:"path"`
	TargetBranch string `json:"target_branch"`
	BaseBranch   string `json:"base_branch"`
	Push         bool   `json:"push"`
}

func handleMerge(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var req MergeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "Streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	logStep := func(step string, out string, err error) error {
		msg := fmt.Sprintf("==> %s\n%s\n", step, out)
		if err != nil {
			msg += fmt.Sprintf("ERROR: %v\n\n", err)
		} else {
			msg += "\n"
		}
		fmt.Fprint(w, msg)
		flusher.Flush()
		return err
	}

	baseBranch := "origin/" + req.BaseBranch
	targetBranch := req.TargetBranch

	fmt.Fprintf(w, "> Processando projeto em: %s\n", req.Path)
	flusher.Flush()

	out, err := runGitCommand(req.Path, "fetch", "--all", "--prune")
	logStep("git fetch --all --prune", out, err)
	if err != nil {
		return
	}

	// Tenta abortar qualquer merge que tenha ficado pendente (ex: conflito em execução anterior)
	outAb, errAb := runGitCommand(req.Path, "merge", "--abort")
	if errAb == nil {
		logStep("git merge --abort", outAb, nil)
	}

	// Força a limpeza de qualquer alteração pendente (arquivos modificados e untracked)
	outRes, _ := runGitCommand(req.Path, "reset", "--hard", "HEAD")
	logStep("git reset --hard HEAD", outRes, nil)

	outClean, _ := runGitCommand(req.Path, "clean", "-fd")
	logStep("git clean -fd", outClean, nil)

	// Desanexa o HEAD para podermos recriar a branch sem travas
	out, err = runGitCommand(req.Path, "switch", "--detach", "HEAD")
	err = logStep("git switch --detach HEAD", out, err)
	if err != nil {
		return
	}

	out, _ = runGitCommand(req.Path, "branch", "-D", "quality-assurance")
	logStep("git branch -D quality-assurance", out, nil)

	// Força a criação da branch baseada no target (ignorando o que tinha lá com -f)
	out, err = runGitCommand(req.Path, "checkout", "-f", "-B", "quality-assurance", baseBranch)
	err = logStep(fmt.Sprintf("git checkout -f -B quality-assurance %s", baseBranch), out, err)
	if err != nil {
		return
	}

	out, err = runGitCommand(req.Path, "merge", "--no-ff", "-m", fmt.Sprintf("Merge %s into quality-assurance", targetBranch), targetBranch)
	err = logStep(fmt.Sprintf("git merge %s", targetBranch), out, err)
	if err != nil {
		fmt.Fprint(w, "\n[SISTEMA] ❌ Falha no merge. Verifique conflitos.\n")
		flusher.Flush()
		return
	}

	if req.Push {
		out, err = runGitCommand(req.Path, "push", "-f", "origin", "quality-assurance")
		err = logStep("git push -f origin quality-assurance", out, err)
		if err != nil {
			fmt.Fprint(w, "\n[SISTEMA] ❌ Falha no push.\n")
			flusher.Flush()
			return
		}
	}

	fmt.Fprint(w, "\n[SISTEMA] 🎉 Merge processado com sucesso!\n")
	flusher.Flush()
}
