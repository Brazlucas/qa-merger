package main

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/layout"
	"fyne.io/fyne/v2/widget"
)

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
	a := app.New()
	w := a.NewWindow("QA Merger - Auto Merge")
	w.Resize(fyne.NewSize(750, 600))

	var selectedProject string

	projectLabel := widget.NewLabel("Nenhum projeto selecionado")
	projectLabel.TextStyle = fyne.TextStyle{Bold: true}

	// 1. Target Branch (Autocomplete Select)
	targetBranchSelect := widget.NewSelectEntry([]string{})
	targetBranchSelect.PlaceHolder = "-- Buscar e selecionar branch remota --"

	// 2. Base Branch (Select with 2 options)
	baseBranchSelect := widget.NewSelect([]string{"develop", "master"}, nil)
	baseBranchSelect.SetSelected("master")

	// 3. Push checkbox
	pushCheck := widget.NewCheck("Efetuar Push (Remote)", nil)
	pushCheck.Checked = false

	// Log Output
	logOutput := widget.NewMultiLineEntry()
	logOutput.Wrapping = fyne.TextWrapWord
	logOutput.Disable()
	
	// Add text to log and scroll to bottom
	appendLog := func(text string) {
		logOutput.SetText(logOutput.Text + text)
		// Very simple auto-scroll by moving cursor to end
		lines := strings.Split(logOutput.Text, "\n")
		logOutput.CursorRow = len(lines) - 1
		logOutput.CursorColumn = 0
	}

	fetchBranches := func(dir string) {
		selectedProject = dir
		projectLabel.SetText("Projeto: " + dir)
		logOutput.SetText("> Atualizando fetch e buscando branches ativas remotas...\n")
		
		go func() {
			out, err := runGitCommand(dir, "fetch", "--all", "--prune")
			if err != nil {
				appendLog("\nErro no fetch:\n" + out)
				return
			}
			
			out, err = runGitCommand(dir, "branch", "-r", "--format=%(refname:short)")
			if err != nil {
				appendLog("\nErro ao listar branches:\n" + out)
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
			
			targetBranchSelect.SetOptions(branches)
			appendLog(fmt.Sprintf("\n> OK! Encontradas %d branches de origin.\n", len(branches)))
		}()
	}

	// Folder Picker Button
	selectFolderBtn := widget.NewButton("Selecionar Projeto Front-End", func() {
		dialog.ShowFolderOpen(func(lu fyne.ListableURI, err error) {
			if err != nil || lu == nil {
				return
			}
			fetchBranches(lu.Path())
		}, w)
	})

	logStep := func(step string, out string, err error) error {
		msg := fmt.Sprintf("==> %s\n%s\n", step, out)
		if err != nil {
			msg += fmt.Sprintf("ERROR: %v\n\n", err)
		} else {
			msg += "\n"
		}
		appendLog(msg)
		return err
	}

	mergeBtn := widget.NewButton("Aprovar e Mesclar em QA", func() {
		if selectedProject == "" {
			dialog.ShowInformation("Erro", "Selecione um projeto primeiro.", w)
			return
		}
		if targetBranchSelect.Text == "" {
			dialog.ShowInformation("Erro", "Selecione a branch de testes.", w)
			return
		}
		if baseBranchSelect.Selected == "" {
			dialog.ShowInformation("Erro", "Selecione a origem da QA.", w)
			return
		}

		baseBranch := "origin/" + baseBranchSelect.Selected
		targetBranch := targetBranchSelect.Text
		push := pushCheck.Checked

		logOutput.SetText(fmt.Sprintf("[SISTEMA] Iniciando fluxo de Merge em QA...\n- Dir: %s\n- Branch QA Origem: %s\n- Target: %s\n\n", selectedProject, baseBranch, targetBranch))

		go func() {
			out, err := runGitCommand(selectedProject, "fetch", "--all", "--prune")
			logStep("git fetch --all --prune", out, err)
			if err != nil { return }

			out, err = runGitCommand(selectedProject, "switch", "--detach", "HEAD")
			logStep("git switch --detach HEAD", out, err)

			out, err = runGitCommand(selectedProject, "branch", "-D", "quality-assurance")
			logStep("git branch -D quality-assurance", out, nil) // ignore error here

			out, err = runGitCommand(selectedProject, "checkout", "-b", "quality-assurance", baseBranch)
			err = logStep(fmt.Sprintf("git checkout -b quality-assurance %s", baseBranch), out, err)
			if err != nil { return }

			out, err = runGitCommand(selectedProject, "merge", "--no-ff", "-m", fmt.Sprintf("Merge %s into quality-assurance", targetBranch), targetBranch)
			err = logStep(fmt.Sprintf("git merge %s", targetBranch), out, err)
			if err != nil {
				appendLog("\n[SISTEMA] ❌ Falha no merge. Verifique conflitos.")
				return
			}

			if push {
				out, err = runGitCommand(selectedProject, "push", "-f", "origin", "quality-assurance")
				err = logStep("git push -f origin quality-assurance", out, err)
				if err != nil {
					appendLog("\n[SISTEMA] ❌ Falha no push.")
					return
				}
			}

			appendLog("\n[SISTEMA] 🎉 Merge processado com sucesso!")
		}()
	})
	mergeBtn.Importance = widget.HighImportance

	// Build the form layout
	form := container.NewVBox(
		projectLabel,
		selectFolderBtn,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("1. Escolha a Branch de Testes", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		targetBranchSelect,
		widget.NewSeparator(),
		widget.NewLabelWithStyle("2. Origem da Nova QA", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		baseBranchSelect,
		widget.NewSeparator(),
		pushCheck,
		layout.NewSpacer(),
		mergeBtn,
	)

	// Build the right log panel
	logPanel := container.NewBorder(
		widget.NewLabelWithStyle("Terminal Output", fyne.TextAlignLeading, fyne.TextStyle{Bold: true}),
		nil, nil, nil, logOutput,
	)

	split := container.NewHSplit(
		container.NewPadded(form),
		container.NewPadded(logPanel),
	)
	split.SetOffset(0.4)

	w.SetContent(split)
	w.ShowAndRun()
}
