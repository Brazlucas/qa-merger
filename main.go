package main

import (
	"bytes"
	"embed"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
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

func runMerge(req MergeRequest, logFn func(step, out string, err error)) error {
	baseBranch := "origin/" + req.BaseBranch
	targetBranch := req.TargetBranch

	step := func(name, out string, err error) error {
		logFn(name, out, err)
		return err
	}

	out, err := runGitCommand(req.Path, "fetch", "--all", "--prune")
	if err := step("git fetch --all --prune", out, err); err != nil {
		return err
	}

	outAb, errAb := runGitCommand(req.Path, "merge", "--abort")
	if errAb == nil {
		step("git merge --abort", outAb, nil)
	}

	outRes, _ := runGitCommand(req.Path, "reset", "--hard", "HEAD")
	step("git reset --hard HEAD", outRes, nil)

	outClean, _ := runGitCommand(req.Path, "clean", "-fd")
	step("git clean -fd", outClean, nil)

	out, err = runGitCommand(req.Path, "switch", "--detach", "HEAD")
	if err := step("git switch --detach HEAD", out, err); err != nil {
		return err
	}

	out, _ = runGitCommand(req.Path, "branch", "-D", "quality-assurance")
	step("git branch -D quality-assurance", out, nil)

	out, err = runGitCommand(req.Path, "checkout", "-f", "-B", "quality-assurance", baseBranch)
	if err := step(fmt.Sprintf("git checkout -f -B quality-assurance %s", baseBranch), out, err); err != nil {
		return err
	}

	out, err = runGitCommand(req.Path, "merge", "--no-ff", "-m", fmt.Sprintf("Merge %s into quality-assurance", targetBranch), targetBranch)
	if err := step(fmt.Sprintf("git merge %s", targetBranch), out, err); err != nil {
		return err
	}

	if req.Push {
		out, err = runGitCommand(req.Path, "push", "-f", "origin", "quality-assurance")
		if err := step("git push -f origin quality-assurance", out, err); err != nil {
			return err
		}
	}

	return nil
}

func main() {
	projectFlag := flag.String("project", "", "Caminho ou nome do projeto (relativo ao diretório atual)")
	branchFlag := flag.String("branch", "", "Branch alvo para mergear em quality-assurance")
	baseFlag := flag.String("base", "master", "Branch base (padrão: master)")
	pushFlag := flag.Bool("push", false, "Fazer push forçado para origin após o merge")
	flag.Parse()

	// Modo CLI: executa merge direto no terminal sem abrir o servidor web
	if *projectFlag != "" && *branchFlag != "" {
		projectPath := *projectFlag
		if !filepath.IsAbs(projectPath) {
			cwd, err := os.Getwd()
			if err != nil {
				log.Fatal(err)
			}
			projectPath = filepath.Join(cwd, projectPath)
		}

		targetBranch := *branchFlag
		if !strings.HasPrefix(targetBranch, "origin/") {
			targetBranch = "origin/" + targetBranch
		}

		req := MergeRequest{
			Path:         projectPath,
			TargetBranch: targetBranch,
			BaseBranch:   *baseFlag,
			Push:         *pushFlag,
		}

		fmt.Printf("> Projeto: %s\n> Branch: %s → quality-assurance (base: %s)\n\n", projectPath, *branchFlag, *baseFlag)

		err := runMerge(req, func(step, out string, err error) {
			fmt.Printf("==> %s\n%s\n", step, out)
			if err != nil {
				fmt.Printf("ERROR: %v\n\n", err)
			}
		})

		if err != nil {
			fmt.Println("\n❌ Falha no merge. Verifique conflitos.")
			os.Exit(1)
		}
		fmt.Println("\n✅ Merge processado com sucesso!")
		return
	}

	// Modo Web: comportamento original
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
		time.Sleep(500 * time.Millisecond)
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

	fmt.Fprintf(w, "> Processando projeto em: %s\n", req.Path)
	flusher.Flush()

	err := runMerge(req, func(step, out string, err error) {
		msg := fmt.Sprintf("==> %s\n%s\n", step, out)
		if err != nil {
			msg += fmt.Sprintf("ERROR: %v\n\n", err)
		} else {
			msg += "\n"
		}
		fmt.Fprint(w, msg)
		flusher.Flush()
	})

	if err != nil {
		fmt.Fprint(w, "\n[SISTEMA] ❌ Falha no merge. Verifique conflitos.\n")
		flusher.Flush()
		return
	}

	fmt.Fprint(w, "\n[SISTEMA] 🎉 Merge processado com sucesso!\n")
	flusher.Flush()
}
