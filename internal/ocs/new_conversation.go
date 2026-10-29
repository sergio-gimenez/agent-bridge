package ocs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

type newConversation struct {
	// The directory the conversation starts in, as typed or as chosen from
	// the suggestions.
	directory string
	// What the suggestions are filtered by: the directory as last typed, so
	// moving through them does not refilter the list under the cursor.
	query string
	// The highlighted suggestion, -1 when the directory is as typed.
	choice   int
	projects []dirSuggestion
	// The suggestions for query, recomputed only when it changes.
	suggestions  []dirSuggestion
	suggestedFor *string
	target       int
	err          string
	yolo         *bool
	// Set when the dialog opens this conversation in another account (Ctrl+T)
	// instead of starting a blank one: it keeps its directory, so there is no
	// directory to choose.
	session *Session
	// Screen rows of the first suggestion and the first account, for clicks;
	// 0 when not drawn. dirOffset is the first suggestion shown.
	dirRow, dirOffset, dirShown, accountRow int
	// Ctrl+S: a secret conversation, in OpenCode on one of the pickable
	// models (model indexes them; -1 when there is none).
	secret      bool
	models      []SecretModel
	model       int
	modelOffset int
	modelShown  int
	// The directory before secret mode cleared it, back when it is off.
	directoryBefore string
}

func (p *picker) openNew(directory string) {
	if directory == "" {
		directory, _ = os.Getwd()
	}
	target := p.indexOfTarget(p.currentTarget())
	if target < 0 {
		target = 0
	}
	dialog := &newConversation{directory: ShortenHome(directory), target: target, yolo: p.yolo, choice: -1, projects: p.recentProjects()}
	for i, project := range dialog.dirSuggestions() {
		if project.path == filepath.Clean(directory) {
			dialog.choice = i
		}
	}
	p.newConversation = dialog
	p.refreshStaleUsage()
}

// openIn is the same dialog for choosing where the selected conversation
// goes, with the limits beside every account instead of cycling blind.
func (p *picker) openIn() {
	selected := p.selected()
	if selected == nil {
		return
	}
	session := *selected
	target := max(0, p.indexOfTarget(p.currentTarget()))
	p.newConversation = &newConversation{session: &session, target: target, yolo: p.yolo, choice: -1}
	p.refreshStaleUsage()
}

// refreshStaleUsage asks the background refresh for new limits now when the
// ones shown are older than the cache allows.
func (p *picker) refreshStaleUsage() {
	if p.usageKick == nil || p.usageLoading || usageFresh(p.usage, p.now()) {
		return
	}
	select {
	case p.usageKick <- struct{}{}:
		p.usageLoading = true
	default:
	}
}

func (dialog *newConversation) dirSuggestions() []dirSuggestion {
	if dialog.suggestedFor == nil || *dialog.suggestedFor != dialog.query {
		query := dialog.query
		dialog.suggestions, dialog.suggestedFor = suggestDirectories(dialog.projects, query), &query
	}
	return dialog.suggestions
}

// typed sets the directory to what was typed and filters the suggestions by it.
func (dialog *newConversation) typed(directory string) {
	dialog.directory, dialog.query, dialog.choice, dialog.err = directory, directory, -1, ""
}

// choose highlights suggestion index, -1 going back to what was typed.
func (dialog *newConversation) choose(index int) {
	suggestions := dialog.dirSuggestions()
	dialog.choice, dialog.err = clamp(index, -1, len(suggestions)-1), ""
	if dialog.choice < 0 {
		if dialog.query != "" {
			dialog.directory = dialog.query
		}
		return
	}
	dialog.directory = ShortenHome(suggestions[dialog.choice].path)
}

func (p *picker) newYoloFor(target *Account) bool {
	if p.newConversation.yolo != nil {
		return *p.newConversation.yolo
	}
	return p.yoloFor(target)
}

// pickableModels is the head of the list: SecretModels sorts the models a
// secret conversation may use first.
func (dialog *newConversation) pickableModels() []SecretModel {
	count := 0
	for count < len(dialog.models) && dialog.models[count].Tier.Pickable() {
		count++
	}
	return dialog.models[:count]
}

func (p *picker) toggleSecret() {
	dialog := p.newConversation
	dialog.err = ""
	if dialog.secret {
		dialog.secret = false
		dialog.typed(dialog.directoryBefore)
		return
	}
	if dialog.models == nil {
		dialog.models = SecretModels(p.options.Config, p.now())
	}
	dialog.secret, dialog.model = true, -1
	pickable := dialog.pickableModels()
	for i, model := range pickable {
		if model.ID == p.options.Config.Secret.Default {
			dialog.model = i
		}
	}
	if dialog.model < 0 && len(pickable) > 0 {
		dialog.model = 0
	}
	// A secret conversation is usually not about a project: blank starts it
	// in a scratch directory.
	dialog.directoryBefore = dialog.directory
	dialog.typed("")
}

func (dialog *newConversation) cycleModel(step int) {
	if count := len(dialog.pickableModels()); count > 0 {
		dialog.model = NextTarget(count, dialog.model, step)
	}
}

func (p *picker) finishSecret() (*PickResult, error) {
	dialog := p.newConversation
	pickable := dialog.pickableModels()
	if dialog.model < 0 || dialog.model >= len(pickable) {
		dialog.err = "No model is private enough: add a local provider or sign in to OpenCode Go"
		return nil, nil
	}
	directory := ""
	if strings.TrimSpace(dialog.directory) != "" {
		var err error
		directory, err = filepath.Abs(expandHome(dialog.directory))
		if err == nil {
			if info, statErr := os.Stat(directory); statErr != nil || !info.IsDir() {
				err = fmt.Errorf("Directory does not exist: %s", directory)
			}
		}
		if err != nil {
			dialog.err = err.Error()
			return nil, nil
		}
	}
	target := p.options.Config.Opencode
	if target.Tool == "" {
		target = OpencodeAccount
	}
	return &PickResult{
		Session:         Session{Directory: directory, Source: SourceOpencode},
		Target:          target,
		Mode:            ModeNew,
		SkipPermissions: dialog.yolo,
		Secret:          pickable[dialog.model].ID,
	}, nil
}

func (p *picker) newTarget() *Account {
	if len(p.targets) == 0 {
		return nil
	}
	return &p.targets[p.newConversation.target]
}

func (p *picker) handleNew(key Key) (*PickResult, error) {
	dialog := p.newConversation
	if dialog.session != nil {
		return p.handleOpenIn(key)
	}
	switch {
	case key.Ctrl && key.Name == "c":
		return nil, ErrCancelled
	case key.Name == "escape":
		p.newConversation = nil
	case key.Ctrl && key.Name == "s":
		p.toggleSecret()
	case dialog.secret && key.Name == "enter":
		return p.finishSecret()
	case dialog.secret && (key.Name == "tab" || key.Name == "right" || key.Ctrl && key.Name == "t"):
		dialog.cycleModel(1)
	case dialog.secret && (key.Name == "backtab" || key.Name == "left"):
		dialog.cycleModel(-1)
	case dialog.secret && key.Ctrl && key.Name == "g":
		// No suggested account: a secret conversation is always OpenCode.
	case key.Name == "enter":
		target := p.newTarget()
		if target == nil {
			dialog.err = "Configure an agent account first"
			return nil, nil
		}
		if strings.TrimSpace(dialog.directory) == "" {
			dialog.err = "Enter a project directory"
			return nil, nil
		}
		directory, err := filepath.Abs(expandHome(dialog.directory))
		if err == nil {
			info, statErr := os.Stat(directory)
			if statErr != nil || !info.IsDir() {
				err = fmt.Errorf("Directory does not exist: %s", directory)
			}
		}
		if err != nil {
			dialog.err = err.Error()
			return nil, nil
		}
		result := p.finish(Session{Directory: directory, Source: target.Tool}, target, ModeNew)
		result.SkipPermissions = dialog.yolo
		return &result, nil
	case key.Name == "tab", key.Name == "right", key.Ctrl && key.Name == "t":
		dialog.target = NextTarget(len(p.targets), dialog.target, 1)
	case key.Name == "backtab", key.Name == "left":
		dialog.target = NextTarget(len(p.targets), dialog.target, -1)
	case key.Name == "up":
		dialog.choose(dialog.choice - 1)
	case key.Name == "down":
		dialog.choose(dialog.choice + 1)
	case key.Name == "pageup":
		dialog.choose(max(0, dialog.choice-max(1, dialog.dirShown)))
	case key.Name == "pagedown":
		dialog.choose(dialog.choice + max(1, dialog.dirShown))
	case key.Ctrl && key.Name == "g":
		if suggested := p.indexOfTarget(p.recommendedTarget()); suggested >= 0 {
			dialog.target = suggested
		}
	case key.Ctrl && key.Name == "y":
		value := !p.newYoloFor(p.newTarget())
		dialog.yolo = &value
	case key.Ctrl && key.Name == "u":
		dialog.typed("")
	case key.Name == "backspace":
		runes := []rune(dialog.directory)
		if len(runes) > 0 {
			runes = runes[:len(runes)-1]
		}
		dialog.typed(string(runes))
	case key.Text != "" && !key.Ctrl:
		dialog.typed(dialog.directory + key.Text)
	}
	return nil, nil
}

func (p *picker) handleOpenIn(key Key) (*PickResult, error) {
	dialog := p.newConversation
	switch {
	case key.Ctrl && key.Name == "c":
		return nil, ErrCancelled
	case key.Name == "escape":
		p.newConversation = nil
	case key.Name == "enter", key.Ctrl && key.Name == "f":
		target := p.newTarget()
		if target == nil {
			dialog.err = "Configure an agent account first"
			return nil, nil
		}
		mode := ModeResume
		if key.Name == "f" {
			mode = ModeFork
		}
		result := p.finish(*dialog.session, target, mode)
		result.SkipPermissions = dialog.yolo
		return &result, nil
	case key.Name == "tab", key.Name == "right", key.Ctrl && key.Name == "t":
		dialog.target = NextTarget(len(p.targets), dialog.target, 1)
	case key.Name == "backtab", key.Name == "left":
		dialog.target = NextTarget(len(p.targets), dialog.target, -1)
	case key.Name == "up":
		dialog.target = clampIndex(dialog.target-1, len(p.targets))
	case key.Name == "down":
		dialog.target = clampIndex(dialog.target+1, len(p.targets))
	case key.Ctrl && key.Name == "g":
		if suggested := p.indexOfTarget(p.recommendedTarget()); suggested >= 0 {
			dialog.target = suggested
		}
	case key.Ctrl && key.Name == "y":
		value := !p.newYoloFor(p.newTarget())
		dialog.yolo = &value
	}
	return nil, nil
}

// clickNew handles a click in the dialog's suggestions or accounts.
func (p *picker) clickNew(y int) bool {
	dialog := p.newConversation
	bottom := 3 + p.mouseBodyRows - 1 // the box's bottom border
	switch {
	case y >= bottom:
		return false
	case dialog.dirRow > 0 && y >= dialog.dirRow && y < dialog.dirRow+dialog.dirShown:
		dialog.choose(dialog.dirOffset + y - dialog.dirRow)
	case dialog.secret && dialog.accountRow > 0 && y >= dialog.accountRow && y < dialog.accountRow+dialog.modelShown:
		dialog.model = dialog.modelOffset + y - dialog.accountRow
	case !dialog.secret && dialog.accountRow > 0 && y >= dialog.accountRow && y < dialog.accountRow+len(p.targets):
		dialog.target = y - dialog.accountRow
	default:
		return false
	}
	return true
}

func (p *picker) drawNew(lines []string, width, bodyRows int) {
	dialog := p.newConversation
	if dialog.session != nil {
		p.drawOpenIn(lines, width, bodyRows)
		return
	}
	if dialog.secret {
		p.drawSecret(lines, width, bodyRows)
		return
	}
	inner := width - 4
	target := p.newTarget()
	label := "No configured accounts"
	if target != nil {
		label = destinationName(target)
	}
	permissions := "asks for permissions"
	if p.newYoloFor(target) {
		permissions = "YOLO: bypasses permission checks"
	}
	body := []string{
		bold(truncatePlain(label, inner/2)) + dim(" · "+truncatePlain(permissions, inner/2)),
		"",
		bold("Directory ") + truncatePlain(dialog.directory+"▏", max(1, inner-10)),
	}
	if dialog.err != "" {
		body = append(body, red(truncatePlain(dialog.err, inner)))
	}

	// What stays below the suggestions, so they take only the rows left over,
	// but always a few, cutting the help instead.
	below := 2
	if len(p.targets) > 0 {
		below += 2 + len(p.targets)
	}
	suggestions := dialog.dirSuggestions()
	dialog.dirShown = min(len(suggestions), clamp(bodyRows-2-len(body)-below, 3, 8))
	if dialog.choice >= dialog.dirOffset+dialog.dirShown {
		dialog.dirOffset = dialog.choice - dialog.dirShown + 1
	}
	dialog.dirOffset = clamp(min(dialog.dirOffset, dialog.choice), 0, max(0, len(suggestions)-dialog.dirShown))
	// The box's top border sits on screen row 3, so body line i is on row 4+i.
	dialog.dirRow = 4 + len(body)
	if len(suggestions) == 0 {
		dialog.dirRow = 0
		body = append(body, dim("  no matching directory · type a path like ~/src/"))
	}
	for i := dialog.dirOffset; i < dialog.dirOffset+dialog.dirShown; i++ {
		body = append(body, p.dirRow(suggestions[i], i == dialog.choice, inner))
	}
	if more := len(suggestions) - dialog.dirOffset - dialog.dirShown; more > 0 {
		body = append(body, dim(fmt.Sprintf("  … %d more", more)))
	}

	dialog.accountRow = 0
	if len(p.targets) > 0 {
		header, rows := p.limitRows(dialog.target, inner)
		body = append(body, "", header)
		dialog.accountRow = 4 + len(body)
		body = append(body, rows...)
	}
	body = append(body, "", dim(truncatePlain("type or ↑↓ for the directory · tab / ←→ / click for the account · ^g suggested · ^u clear · ^y yolo · ^s secret", inner)))
	box := boxed("New conversation", body, width, bodyRows)
	for i := 0; i < bodyRows; i++ {
		lines[i+2] = box[i]
	}
}

// drawSecret is the dialog with Ctrl+S on: the directory, blank for none, and
// the models a secret conversation may use, with the ones left out and why.
func (p *picker) drawSecret(lines []string, width, bodyRows int) {
	dialog := p.newConversation
	inner := width - 4
	permissions := "asks for permissions"
	if p.newYoloFor(&OpencodeAccount) {
		permissions = "YOLO: bypasses permission checks"
	}
	directory := dialog.directory + "▏"
	if dialog.directory == "" {
		directory += dim(" blank: a scratch directory, no project")
	}
	body := []string{
		bold("OpenCode ") + magenta("SECRET") + dim(truncatePlain(" · "+permissions, max(1, inner-15))),
		dim(truncatePlain("Nothing is kept: the store lives in RAM, without plugins or MCPs, and is deleted when OpenCode exits.", inner)),
		"",
		bold("Directory ") + truncatePlain(directory, max(1, inner-10)),
	}
	if dialog.err != "" {
		body = append(body, red(truncatePlain(dialog.err, inner)))
	}

	pickable := dialog.pickableModels()
	excluded := secretExclusions(dialog.models[len(pickable):], inner)
	// Rows below the models: the blank line, what is left out, the blank
	// line and the help.
	below := 2 + len(excluded) + 2

	suggestions := dialog.dirSuggestions()
	dialog.dirShown = min(len(suggestions), clamp(bodyRows-2-len(body)-below-2-len(pickable), 2, 4))
	if dialog.choice >= dialog.dirOffset+dialog.dirShown {
		dialog.dirOffset = dialog.choice - dialog.dirShown + 1
	}
	dialog.dirOffset = clamp(min(dialog.dirOffset, dialog.choice), 0, max(0, len(suggestions)-dialog.dirShown))
	dialog.dirRow = 4 + len(body)
	if len(suggestions) == 0 {
		dialog.dirRow = 0
	}
	for i := dialog.dirOffset; i < dialog.dirOffset+dialog.dirShown; i++ {
		body = append(body, p.dirRow(suggestions[i], i == dialog.choice, inner))
	}

	header := "Model"
	if policy := " OpenCode Go privacy table " + opencodeGoPolicyDate + " · " + opencodeGoPolicySource; inner > len(policy)+10 {
		header = alignRight(bold(header), dim(policy), inner)
	} else {
		header = bold(header)
	}
	body = append(body, "", header)
	dialog.accountRow = 4 + len(body)
	dialog.modelShown = min(len(pickable), max(1, bodyRows-2-len(body)-below))
	if dialog.model >= dialog.modelOffset+dialog.modelShown {
		dialog.modelOffset = dialog.model - dialog.modelShown + 1
	}
	dialog.modelOffset = clamp(min(dialog.modelOffset, max(0, dialog.model)), 0, max(0, len(pickable)-dialog.modelShown))
	if len(pickable) == 0 {
		dialog.accountRow = 0
		body = append(body, dim("  none: add a local provider to OpenCode, sign in to OpenCode Go, or vouch for one in agb's secret.models"))
	}
	for i := dialog.modelOffset; i < dialog.modelOffset+dialog.modelShown; i++ {
		body = append(body, secretModelRow(pickable[i], i == dialog.model, inner))
	}
	if more := len(pickable) - dialog.modelOffset - dialog.modelShown; more > 0 {
		body = append(body, dim(fmt.Sprintf("  … %d more", more)))
	}
	if len(excluded) > 0 {
		body = append(body, "", dim("Not offered"))
		body = append(body, excluded...)
	}
	body = append(body, "", dim(truncatePlain("type or ↑↓ for the directory · tab / ←→ / click for the model · ^u clear · ^y yolo · ^s secret off", inner)))
	box := boxed("New secret conversation", body, width, bodyRows)
	for i := 0; i < bodyRows; i++ {
		lines[i+2] = box[i]
	}
}

func secretModelRow(model SecretModel, chosen bool, inner int) string {
	marker := "  "
	if chosen {
		marker = accent("▸ ")
	}
	label := cyan(fmt.Sprintf("%-5s", model.Tier.Label()))
	if model.Tier == SecretLocal {
		label = green(fmt.Sprintf("%-5s", model.Tier.Label()))
	}
	id := truncatePlain(model.ID, max(1, inner/2))
	if chosen {
		id = bold(id)
	}
	return alignRight(marker+label+" "+id, dim(truncatePlain(model.Note, max(1, inner/2-10))), inner)
}

// secretExclusions is one line per reason a model is left out, naming a whole
// provider as provider/* when every model it has is left out for that reason.
func secretExclusions(models []SecretModel, inner int) []string {
	perProvider := map[string]int{}
	for _, model := range models {
		perProvider[model.Provider()]++
	}
	var reasons []string
	byReason := map[string][]SecretModel{}
	for _, model := range models {
		reason := model.Tier.Label() + " " + model.Note
		if _, ok := byReason[reason]; !ok {
			reasons = append(reasons, reason)
		}
		byReason[reason] = append(byReason[reason], model)
	}
	var lines []string
	for _, reason := range reasons {
		group := byReason[reason]
		var names []string
		if provider := group[0].Provider(); len(group) == perProvider[provider] && len(group) > 1 {
			names = []string{provider + "/*"}
		} else {
			for _, model := range group {
				names = append(names, model.ID)
			}
		}
		_, note, _ := strings.Cut(reason, " ")
		lines = append(lines, "  "+dim(truncatePlain(note+": "+strings.Join(names, ", "), inner-2)))
	}
	return lines
}

func (p *picker) drawOpenIn(lines []string, width, bodyRows int) {
	dialog := p.newConversation
	session := dialog.session
	inner := width - 4
	target := p.newTarget()
	label := "No configured accounts"
	route := ""
	if target != nil {
		label = destinationName(target)
		if !IsNativeTarget(session, target) {
			route = " · seeded from " + sessionLabel(session)
		}
	}
	permissions := "asks for permissions"
	if p.newYoloFor(target) {
		permissions = "YOLO: bypasses permission checks"
	}
	body := []string{
		bold(truncatePlain(label, inner/2)) + dim(truncatePlain(route+" · "+permissions, inner/2)),
		dim(truncatePlain(ShortenHome(session.Directory), inner)),
	}
	if dialog.err != "" {
		body = append(body, red(truncatePlain(dialog.err, inner)))
	}
	dialog.dirRow, dialog.accountRow = 0, 0
	if len(p.targets) > 0 {
		header, rows := p.limitRows(dialog.target, inner)
		body = append(body, "", header)
		dialog.accountRow = 4 + len(body)
		body = append(body, rows...)
	}
	body = append(body, "", dim(truncatePlain("↑↓ / tab / click for the account · enter resume · ^f fork · ^g suggested · ^y yolo", inner)))
	title := "Open in…"
	if name := strings.TrimSpace(session.Title); name != "" {
		title = "Open “" + truncatePlain(name, max(1, width/2)) + "” in…"
	}
	box := boxed(title, body, width, bodyRows)
	for i := 0; i < bodyRows; i++ {
		lines[i+2] = box[i]
	}
}

func (p *picker) dirRow(entry dirSuggestion, chosen bool, inner int) string {
	marker := "  "
	if chosen {
		marker = accent("▸ ")
	}
	info := ""
	if entry.sessions > 0 {
		noun := "sessions"
		if entry.sessions == 1 {
			noun = "session"
		}
		info = fmt.Sprintf("%d %s · %s", entry.sessions, noun, relativeTime(entry.updatedMs, p.now()))
	}
	path := truncatePlain(ShortenHome(entry.path), max(1, inner-2-visibleWidth(info)-2))
	if chosen {
		path = bold(path)
	}
	return alignRight(marker+path, dim(info), inner)
}

// limitRows is a header and one line per account: how much of each provider
// window is used, as a bar, so the choice of account is not only the
// suggestion's. When every account names its windows the same way the names go
// in the header once instead of on every row.
func (p *picker) limitRows(selected, inner int) (string, []string) {
	labelWidth, toolWidth := 0, 0
	var names []string
	shared := true
	for i := range p.targets {
		target := &p.targets[i]
		labelWidth = max(labelWidth, visibleWidth(AccountLabel(target.Tool, target)))
		toolWidth = max(toolWidth, visibleWidth(ToolName(target.Tool)))
		usage := p.usage.For(target)
		if usage == nil {
			continue
		}
		for j, window := range usage.Windows {
			if j == len(names) {
				names = append(names, window.Name)
			} else if names[j] != window.Name {
				shared = false
			}
		}
	}
	lead := 2 + labelWidth + 2 + toolWidth + 2
	nameWidth := 0
	if !shared {
		for _, name := range names {
			nameWidth = max(nameWidth, visibleWidth(name)+1)
		}
	}
	// Each window is its name when not shared, the bar, " 100%" and a gap;
	// leave about 30 cells for the reset and the suggestion.
	bar := 8
	if len(names) > 0 {
		bar = clamp((inner-lead-30)/len(names)-nameWidth-7, 4, 24)
	}
	cell := nameWidth + bar + 5

	header := bold(padLine("Account", lead-2)) + "  "
	if shared {
		for _, name := range names {
			header += padLine(dim(name), cell+2)
		}
	}
	if p.usageLoading {
		header += dim(p.spinner() + " checking limits…")
	}
	var rows []string
	for i := range p.targets {
		target := &p.targets[i]
		marker := "  "
		label := padLine(AccountLabel(target.Tool, target), labelWidth)
		if colour := sourceColor[target.Tool]; colour != nil {
			label = colour(label)
		}
		tool := padLine(ToolName(target.Tool), toolWidth)
		if i == selected {
			marker, label, tool = accent("▸ "), bold(label), bold(tool)
		}
		row := marker + label + "  " + tool + "  "
		rows = append(rows, row+p.limitCells(p.usage.For(target), len(names), nameWidth, bar, inner-lead))
	}
	return header, rows
}

// limitCells fits in room cells: the boxed frame cuts plain text only, so a
// styled row must never be longer than the box.
func (p *picker) limitCells(usage *AccountUsage, windows, nameWidth, bar, room int) string {
	if usage == nil {
		return dim(truncatePlain("no limits reported", room))
	}
	if len(usage.Windows) == 0 {
		if usage.Status == UsageBlocked {
			return red("blocked")
		}
		detail := usage.Detail
		if usage.Status == UsageUnknown && strings.Contains(detail, "does not expose") {
			detail = "no limits reported"
		}
		return dim(truncatePlain(detail, room))
	}
	var cells []string
	now := p.now()
	var binding *UsageWindow
	reset := ""
	for i, window := range usage.Windows {
		var cell string
		if window.resetBy(now) {
			// Observed before its reset: the old figure says nothing now.
			cell = dim(strings.Repeat("─", bar)) + " " + padLeft(dim("–"), 4)
			if reset == "" {
				reset = window.Name
			}
		} else {
			if binding == nil || window.UsedPercent > binding.UsedPercent {
				binding = &usage.Windows[i]
			}
			cell = limitBar(window.UsedPercent, bar) + " " + padLeft(fmt.Sprintf("%.0f%%", window.UsedPercent), 4)
		}
		if nameWidth > 0 {
			cell = padLine(dim(window.Name), nameWidth) + cell
		}
		cells = append(cells, cell)
	}
	var tail []string
	if usage.Status == UsageBlocked && binding != nil {
		tail = append(tail, red("blocked"))
	}
	if usage.Recommended {
		tail = append(tail, accent("★ suggested"))
	}
	if binding != nil {
		switch until := untilTime(binding.ResetsAt, now); until {
		case "":
		case "now":
			tail = append(tail, dim(binding.Name+" resets now"))
		default:
			tail = append(tail, dim(binding.Name+" resets in "+until))
		}
	}
	if reset != "" {
		tail = append(tail, dim(reset+" reset since checked"))
	}
	switch {
	case usage.Detail == "cached; "+errUsageSignInExpired.Error():
		tail = append(tail, yellow("sign-in expired: open "+strings.ToUpper(usage.Target)+" to renew"))
	case strings.HasPrefix(usage.Detail, "cached"):
		tail = append(tail, dim("cached"))
	}
	// Pad to the most windows any account has, so the tails line up, then keep
	// as much of the tail as fits, most important first.
	row := padLine(strings.Join(cells, "  "), windows*(nameWidth+bar+7)-2)
	kept := ""
	for _, part := range tail {
		next := part
		if kept != "" {
			next = kept + dim(" · ") + part
		}
		if visibleWidth(row)+2+visibleWidth(next) > room {
			break
		}
		kept = next
	}
	if kept == "" {
		return row
	}
	return row + "  " + kept
}

// limitBar is a thin track width cells long, filled in proportion to used and
// coloured by how close the window is to its limit.
func limitBar(used float64, width int) string {
	filled := clamp(int(used/100*float64(width)+0.5), 0, width)
	if used > 0 && filled == 0 {
		filled = 1
	}
	colour := green
	switch {
	case used >= 90:
		colour = red
	case used >= 70:
		colour = yellow
	}
	return colour(strings.Repeat("━", filled)) + dim(strings.Repeat("─", width-filled))
}

func padLeft(value string, width int) string {
	if gap := width - visibleWidth(value); gap > 0 {
		return strings.Repeat(" ", gap) + value
	}
	return value
}

// untilTime is how long until an RFC3339 instant, as "now", "40m", "3h" or
// "2d", or "" when the instant is unknown.
func untilTime(instant string, now time.Time) string {
	then, err := time.Parse(time.RFC3339, instant)
	if err != nil {
		return ""
	}
	left := then.Sub(now)
	switch {
	case left < time.Minute:
		return "now"
	case left < time.Hour:
		return fmt.Sprintf("%dm", int(left.Minutes()))
	case left < 48*time.Hour:
		return fmt.Sprintf("%dh", int(left.Hours()))
	}
	return fmt.Sprintf("%dd", int(left.Hours()/24))
}
