package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/progress"
	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"
	"github.com/charmbracelet/lipgloss"
)

// ---------- STYLING ----------

var (
	statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#FF5F87")).Bold(true)
	doneStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#00FF00")).Bold(true)
	infoStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("#3C3C3C")).Italic(true)
)

// ---------- ITEM & SSH LIST ----------

type item struct {
	name  string
	isDir bool
	size  int64
}

func (i item) Title() string       { return i.name }
func (i item) Description() string { return "" }
func (i item) FilterValue() string { return i.name }

func listRemote(user, host, path string) ([]list.Item, error) {
	cmd := exec.Command("ssh", user+"@"+host, "ls -l "+path)
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	lines := strings.Split(string(out), "\n")
	var items []list.Item
	for _, l := range lines {
		if l == "" || strings.HasPrefix(l, "total") {
			continue
		}
		fields := strings.Fields(l)
		if len(fields) < 9 {
			continue
		}
		isDir := strings.HasPrefix(fields[0], "d")
		size, _ := strconv.ParseInt(fields[4], 10, 64)
		name := strings.Join(fields[8:], " ")
		items = append(items, item{name: name, isDir: isDir, size: size})
	}
	return items, nil
}

// ---------- WINDOWS DIALOG HELPERS ----------

func getDownloadsPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Downloads")
}

func openFolderDialog(title string) (string, error) {
	dlPath := getDownloadsPath()
	psScript := fmt.Sprintf(`
    Add-Type -AssemblyName System.Windows.Forms
    $f = New-Object System.Windows.Forms.FolderBrowserDialog
    $f.Description = "%s"
    $f.SelectedPath = "%s"
    $f.ShowNewFolderButton = $true
    if($f.ShowDialog() -eq "OK"){
        Write-Output $f.SelectedPath
    }`, title, dlPath)

	out, err := exec.Command("powershell", "-Command", psScript).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func openFileDialog(title string) ([]string, error) {
	dlPath := getDownloadsPath()
	psScript := fmt.Sprintf(`
    Add-Type -AssemblyName System.Windows.Forms
    $f = New-Object System.Windows.Forms.OpenFileDialog
    $f.Title = "%s"
    $f.InitialDirectory = "%s"
    $f.Multiselect = $true
    if($f.ShowDialog() -eq "OK"){
        $f.FileNames
    }`, title, dlPath)

	out, err := exec.Command("powershell", "-Command", psScript).Output()
	if err != nil {
		return nil, err
	}
	raw := strings.TrimSpace(string(out))
	if raw == "" {
		return nil, nil
	}
	return strings.Split(raw, "\r\n"), nil
}

// ---------- TRANSFER PROGRESS UI ----------

type transferMsg struct {
	index int
	err   error
}

type transferModel struct {
	user, host, remotePath, localDest string
	paths                             []string
	isDownload                        bool
	index                             int
	err                               error
	quitting                          bool

	spinner  spinner.Model
	progress progress.Model
}

func (m transferModel) Init() tea.Cmd {
	return tea.Batch(m.spinner.Tick, m.runTransfer(0))
}

func (m transferModel) runTransfer(idx int) tea.Cmd {
	return func() tea.Msg {
		var cmd *exec.Cmd
		local := m.paths[idx]
		remoteFile := filepath.Base(local)

		if m.isDownload {
			fullRemote := fmt.Sprintf("%s@%s:%s/%s", m.user, m.host, m.remotePath, remoteFile)
			cmd = exec.Command("scp", "-r", fullRemote, m.localDest)
		} else {
			fullRemote := fmt.Sprintf("%s@%s:%s", m.user, m.host, m.remotePath)
			cmd = exec.Command("scp", "-r", local, fullRemote)
		}

		err := cmd.Run()
		return transferMsg{index: idx, err: err}
	}
}

func (m transferModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	case transferMsg:
		if msg.err != nil {
			m.err = msg.err
			return m, tea.Quit
		}
		m.index++
		if m.index >= len(m.paths) {
			m.quitting = true
			return m, tea.Quit
		}
		return m, m.runTransfer(m.index)
	}
	return m, nil
}

func (m transferModel) View() string {
	if m.err != nil {
		return statusStyle.Render(fmt.Sprintf("\nTransfer Error: %v\n", m.err))
	}

	if m.index >= len(m.paths) && m.quitting {
		return doneStyle.Render("\n✔ All transfers complete!\n")
	}

	total := len(m.paths)
	currentFile := filepath.Base(m.paths[m.index])
	pct := float64(m.index) / float64(total)

	header := "Uploading"
	if m.isDownload {
		header = "Downloading"
	}

	return fmt.Sprintf(
		"\n %s %s %s %s\n\n %s\n %s %d/%d items\n\n",
		m.spinner.View(),
		statusStyle.Render(header),
		lipgloss.NewStyle().Foreground(lipgloss.Color("#FAFAFA")).Render(currentFile),
		infoStyle.Render(fmt.Sprintf("(Item %d of %d)", m.index+1, total)),
		m.progress.ViewAs(pct),
		infoStyle.Render("Progress:"), m.index, total,
	)
}

// ---------- DOWNLOAD LIST SELECTION ----------

type downloadModel struct {
	list     list.Model
	selected map[string]bool
	quitting bool
}

type downloadDelegate struct {
	model *downloadModel // Pointer to parent model to access 'selected' map
}

func (d downloadDelegate) Height() int                               { return 1 }
func (d downloadDelegate) Spacing() int                              { return 0 }
func (d downloadDelegate) Update(msg tea.Msg, m *list.Model) tea.Cmd { return nil }
func (d downloadDelegate) Render(w io.Writer, m list.Model, index int, listItem list.Item) {
	it := listItem.(item)
	cursor := "  "
	if index == m.Index() {
		cursor = "> "
	}
	checked := "[ ]"
	if d.model != nil && d.model.selected[it.name] {
		checked = "[x]"
	}
	fmt.Fprintf(w, "%s %s %-25s (%d bytes)", cursor, checked, it.name, it.size)
}

// Helper to create the model and link the delegate correctly
func newDownloadModel(items []list.Item) *downloadModel {
	m := &downloadModel{
		selected: make(map[string]bool),
	}
	d := downloadDelegate{model: m}
	m.list = list.New(items, d, 80, 20)
	m.list.Title = "Select items to download"
	return m
}

func (m *downloadModel) Init() tea.Cmd { return nil }
func (m *downloadModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter":
			if it, ok := m.list.SelectedItem().(item); ok {
				m.selected[it.name] = !m.selected[it.name]
			}
		case "c":
			return m, tea.Quit
		case "q", "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		}
	}
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	return m, cmd
}

func (m *downloadModel) View() string {
	return "\n" + m.list.View() + "\n [enter] toggle | [c] confirm | [q] cancel"
}

func (m *downloadModel) getSelectedPaths() []string {
	var paths []string
	for name, ok := range m.selected {
		if ok {
			paths = append(paths, name)
		}
	}
	return paths
}

// ---------- LOGIC HANDLERS ----------

func handleDownload(user, host, remotePath string) {
	dest, _ := openFolderDialog("Select Download Destination")
	if dest == "" {
		fmt.Println("Cancelled.")
		return
	}

	items, err := listRemote(user, host, remotePath)
	if err != nil {
		fmt.Println("Error connecting to server:", err)
		return
	}

	// Correctly initialize model with pointer linking
	selModel := newDownloadModel(items)
	p := tea.NewProgram(selModel)
	finalSel, err := p.Run()

	if err != nil || finalSel == nil {
		return
	}

	m := finalSel.(*downloadModel)
	if m.quitting {
		return
	}

	selectedPaths := m.getSelectedPaths()
	if len(selectedPaths) == 0 {
		fmt.Println("No files selected.")
		return
	}

	// Start the Transfer UI
	t := transferModel{
		user:       user,
		host:       host,
		remotePath: remotePath,
		localDest:  dest,
		paths:      selectedPaths,
		isDownload: true,
		spinner:    spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(statusStyle)),
		progress:   progress.New(progress.WithDefaultGradient()),
	}
	tea.NewProgram(t).Run()
}

func handleUpload(user, host, remotePath string) {
	var uploadType string
	huh.NewSelect[string]().
		Title("Upload Mode").
		Options(huh.NewOption("Files", "f"), huh.NewOption("Folder", "d")).
		Value(&uploadType).
		Run()

	var paths []string
	if uploadType == "d" {
		p, _ := openFolderDialog("Select Folder")
		if p != "" {
			paths = append(paths, p)
		}
	} else {
		paths, _ = openFileDialog("Select Files")
	}

	if len(paths) == 0 {
		return
	}

	t := transferModel{
		user:       user,
		host:       host,
		remotePath: remotePath,
		paths:      paths,
		isDownload: false,
		spinner:    spinner.New(spinner.WithSpinner(spinner.Dot), spinner.WithStyle(statusStyle)),
		progress:   progress.New(progress.WithDefaultGradient()),
	}
	tea.NewProgram(t).Run()
}

// ---------- MAIN ----------

func main() {
	var user string
	host, remotePath := "", ""

	huh.NewInput().
		Title("Remote Login").
		Prompt("Username: ").
		Value(&user).
		Validate(func(s string) error {
			if strings.TrimSpace(s) == "" {
				return errors.New("username is required")
			}
			return nil
		}).
		Run()

	for {
		var choice string
		huh.NewSelect[string]().
			Title("File Transfer Menu").
			Options(
				huh.NewOption("Download Files", "dl"),
				huh.NewOption("Upload Files", "up"),
				huh.NewOption("Quit", "q"),
			).
			Value(&choice).
			Run()

		switch choice {
		case "dl":
			handleDownload(user, host, remotePath)
		case "up":
			handleUpload(user, host, remotePath)
		case "q":
			fmt.Println("Goodbye!")
			return
		}

		fmt.Println("\nAction finished. Press Enter to return to menu...")
		fmt.Scanln()
	}
}
