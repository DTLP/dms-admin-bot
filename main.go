// Command dms-admin-bot is a Telegram bot that manages docker-mailserver
// aliases. It runs commands against the mail server container and exposes
// list/add/delete operations over Telegram, restricted to a single owner.
package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const (
	appName        = "dms-admin-bot"
	execTimeout    = 30 * time.Second
	aliasesPerPage = 10
	// aliasFile is the docker-mailserver virtual alias database inside the
	// container. Reading it directly avoids depending on `setup alias list`
	// output formatting, which varies between docker-mailserver versions.
	aliasFile = "/tmp/docker-mailserver/postfix-virtual.cf"
	// accountsFile is the docker-mailserver mailbox account database inside
	// the container. aliasAdd reads it to refuse aliases whose target is
	// neither a real mailbox nor another alias.
	accountsFile = "/tmp/docker-mailserver/postfix-accounts.cf"
)

type config struct {
	token         string
	userID        int64
	mailContainer string
	mailDomain    string
}

type alias struct {
	address    string
	recipients []string
}

type deleteRequest struct {
	aliasAddr   string
	mailboxAddr string
}

var (
	cfg config

	// localPartRe matches the local part of an email address before the "@".
	localPartRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

	// domainRe matches a mail domain, one or more dot-separated labels. It is
	// used to validate external addresses given to /alias_add in full form.
	domainRe = regexp.MustCompile(`^(?:[a-zA-Z0-9](?:[a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)+[a-zA-Z]{2,}$`)

	// dockerExec runs a command inside the mail container. It is a variable so
	// tests can substitute a fake.
	dockerExec = func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), execTimeout)
		defer cancel()
		cmdArgs := append([]string{"exec", cfg.mailContainer}, args...)
		out, err := exec.CommandContext(ctx, "docker", cmdArgs...).CombinedOutput()
		return string(out), err
	}

	// listFilters remembers the mailbox filter of each /alias_list message so
	// pagination callbacks re-render with the same filter. It is bounded to
	// avoid unbounded growth. Updates are handled sequentially, so no locking
	// is needed.
	listFilters = map[int]string{}

	// pendingDelete holds alias deletions awaiting confirmation, keyed by the
	// confirmation message id.
	pendingDelete = map[int]deleteRequest{}

	// lastUnauthorizedLog throttles log output from spammers so one flooded
	// user produces at most one log line per minute.
	lastUnauthorizedLog = map[int64]time.Time{}
)

func main() {
	if err := loadConfig(); err != nil {
		log.Fatalf("config: %v", err)
	}

	bot, err := tgbotapi.NewBotAPI(cfg.token)
	if err != nil {
		log.Fatalf("telegram: %v", err)
	}
	log.Printf("authorized on account %s", bot.Self.UserName)

	if err := registerCommands(bot); err != nil {
		log.Printf("failed to register commands: %v", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		switch {
		case update.Message != nil:
			handleMessage(bot, update.Message)
		case update.CallbackQuery != nil:
			handleCallback(bot, update.CallbackQuery)
		}
	}
}

func loadConfig() error {
	var err error
	cfg.token = strings.TrimSpace(os.Getenv("BOT_TOKEN"))
	cfg.mailContainer = strings.TrimSpace(os.Getenv("MAIL_CONTAINER"))
	cfg.mailDomain = strings.TrimSpace(os.Getenv("MAIL_DOMAIN"))

	if cfg.token == "" {
		return fmt.Errorf("BOT_TOKEN is required")
	}
	if cfg.mailContainer == "" {
		return fmt.Errorf("MAIL_CONTAINER is required")
	}
	if cfg.mailDomain == "" {
		return fmt.Errorf("MAIL_DOMAIN is required")
	}
	cfg.userID, err = strconv.ParseInt(strings.TrimSpace(os.Getenv("BOT_USER_ID")), 10, 64)
	if err != nil {
		return fmt.Errorf("BOT_USER_ID must be an integer: %w", err)
	}
	if cfg.userID == 0 {
		return fmt.Errorf("BOT_USER_ID is required")
	}
	return nil
}

// setup executes `setup <args...>` inside the mail container.
func setup(args ...string) (string, error) {
	return dockerExec(append([]string{"setup"}, args...)...)
}

// readAliases returns the aliases stored in the container's virtual alias
// database. A missing or empty database is reported as no aliases.
func readAliases() ([]alias, error) {
	out, err := dockerExec("sh", "-c", "cat "+aliasFile+" 2>/dev/null || true")
	if err != nil {
		return nil, fmt.Errorf("%v: %s", err, out)
	}
	return parseAliases(out), nil
}

// readAccounts returns the set of mailbox addresses in the container's
// account database. It fails closed: a missing database is an error rather
// than "no mailboxes", because then no alias target could be verified.
func readAccounts() (map[string]bool, error) {
	out, err := dockerExec("sh", "-c", "test -f "+accountsFile+" && cat "+accountsFile)
	if err != nil {
		return nil, fmt.Errorf("mailbox database %s not found in container", accountsFile)
	}
	accounts := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if i := strings.Index(line, "|"); i != -1 {
			line = line[:i]
		}
		accounts[strings.ToLower(strings.TrimSpace(line))] = true
	}
	return accounts, nil
}

// mailboxExists reports whether addr is a valid alias target: a real mailbox
// account or another alias. Both resolve through Postfix, which follows
// alias chains, so either is safe to forward to.
func mailboxExists(addr string) (bool, error) {
	accounts, err := readAccounts()
	if err != nil {
		return false, err
	}
	if accounts[strings.ToLower(addr)] {
		return true, nil
	}
	aliases, err := readAliases()
	if err != nil {
		return false, err
	}
	for _, a := range aliases {
		if strings.EqualFold(a.address, addr) {
			return true, nil
		}
	}
	return false, nil
}

func handleMessage(bot *tgbotapi.BotAPI, msg *tgbotapi.Message) {
	if !authorized(msg.From) {
		logUnauthorized(msg.From)
		return
	}
	if !msg.IsCommand() {
		return
	}
	switch msg.Command() {
	case "start":
		sendText(bot, msg.Chat.ID, welcomeText())
	case "help":
		sendText(bot, msg.Chat.ID, helpText())
	case "alias_list":
		aliasList(bot, msg.Chat.ID, msg.CommandArguments())
	case "alias_add":
		aliasAdd(bot, msg.Chat.ID, msg.CommandArguments())
	case "alias_delete":
		aliasDelete(bot, msg.Chat.ID, msg.CommandArguments())
	default:
		sendText(bot, msg.Chat.ID, "Unknown command.\n"+helpText())
	}
}

func aliasAdd(bot *tgbotapi.BotAPI, chatID int64, args string) {
	fields := strings.Fields(args)
	if len(fields) != 2 {
		sendText(bot, chatID, "Usage: /alias_add &lt;alias&gt; &lt;mailbox&gt;")
		return
	}

	aliasLocal, err := normalizeLocalPart(fields[0])
	if err != nil {
		sendText(bot, chatID, "Invalid alias: "+err.Error())
		return
	}
	mailboxAddr, local, err := resolveMailbox(fields[1])
	if err != nil {
		sendText(bot, chatID, "Invalid mailbox: "+err.Error())
		return
	}

	aliasAddr := fmt.Sprintf("%s@%s", aliasLocal, cfg.mailDomain)

	if local {
		if exists, err := mailboxExists(mailboxAddr); err != nil {
			sendText(bot, chatID, "Failed to verify <b>"+escape(mailboxAddr)+"</b>:\n<code>"+escape(err.Error())+"</code>")
			return
		} else if !exists {
			sendText(bot, chatID, "Mailbox <b>"+escape(mailboxAddr)+"</b> does not exist. Create the account or alias first.")
			return
		}
	}

	if out, err := setup("alias", "add", aliasAddr, mailboxAddr); err != nil {
		sendText(bot, chatID, "Failed to add alias:\n<code>"+escape(out)+"</code>")
		return
	}
	sendText(bot, chatID, fmt.Sprintf("Added <b>%s</b> → <b>%s</b>", escape(aliasAddr), escape(mailboxAddr)))
}

func aliasDelete(bot *tgbotapi.BotAPI, chatID int64, args string) {
	fields := strings.Fields(args)
	if len(fields) != 2 {
		sendText(bot, chatID, "Usage: /alias_delete &lt;alias&gt; &lt;mailbox&gt;")
		return
	}

	aliasLocal, err := normalizeLocalPart(fields[0])
	if err != nil {
		sendText(bot, chatID, "Invalid alias: "+err.Error())
		return
	}
	mailboxAddr, _, err := resolveMailbox(fields[1])
	if err != nil {
		sendText(bot, chatID, "Invalid mailbox: "+err.Error())
		return
	}

	aliasAddr := fmt.Sprintf("%s@%s", aliasLocal, cfg.mailDomain)

	row := tgbotapi.NewInlineKeyboardRow(
		tgbotapi.NewInlineKeyboardButtonData("✅ Yes, delete", "confirm_del"),
		tgbotapi.NewInlineKeyboardButtonData("❌ Cancel", "cancel_del"),
	)
	msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Delete <b>%s</b> → <b>%s</b>?", escape(aliasAddr), escape(mailboxAddr)))
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = tgbotapi.NewInlineKeyboardMarkup(row)

	sent, err := bot.Send(msg)
	if err != nil {
		log.Printf("send message: %v", err)
		return
	}
	if len(pendingDelete) >= 1000 {
		pendingDelete = map[int]deleteRequest{}
	}
	pendingDelete[sent.MessageID] = deleteRequest{aliasAddr: aliasAddr, mailboxAddr: mailboxAddr}
}

func aliasList(bot *tgbotapi.BotAPI, chatID int64, args string) {
	filter := strings.TrimSpace(args)
	if filter != "" {
		var err error
		if filter, err = normalizeFilter(filter); err != nil {
			sendText(bot, chatID, "Invalid mailbox: "+err.Error())
			return
		}
	}
	renderAliases(bot, chatID, 0, 0, filter)
}

// renderAliases renders a single page of aliases, optionally filtered to those
// that forward to the given mailbox. A messageID of 0 sends a new message;
// otherwise the existing message is edited in place.
func renderAliases(bot *tgbotapi.BotAPI, chatID int64, messageID, page int, filter string) {
	aliases, err := readAliases()
	if err != nil {
		sendText(bot, chatID, "Failed to read aliases:\n<code>"+escape(err.Error())+"</code>")
		return
	}
	if filter != "" {
		aliases = filterAliases(aliases, filter)
	}
	sort.Slice(aliases, func(i, j int) bool { return aliases[i].address < aliases[j].address })

	if len(aliases) == 0 {
		renderEmptyAliases(bot, chatID, messageID, filter)
		return
	}

	text, nav, _, _ := renderAliasPage(aliases, page, filter)
	kb := tgbotapi.NewInlineKeyboardMarkup(tgbotapi.NewInlineKeyboardRow(nav...))

	if messageID == 0 {
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = kb
		sent, err := bot.Send(msg)
		if err != nil {
			log.Printf("send message: %v", err)
			return
		}
		storeFilter(sent.MessageID, filter)
		return
	}

	storeFilter(messageID, filter)
	edit := tgbotapi.NewEditMessageTextAndMarkup(chatID, messageID, text, kb)
	edit.ParseMode = "HTML"
	if _, err := bot.Send(edit); err != nil {
		log.Printf("edit message: %v", err)
	}
}

// renderAliasPage builds the message text and navigation buttons for one page
// of aliases. It returns the clamped page that is actually shown.
func renderAliasPage(aliases []alias, page int, filter string) (text string, nav []tgbotapi.InlineKeyboardButton, shownPage, totalPages int) {
	if len(aliases) == 0 {
		return "No aliases configured.", nil, 0, 1
	}
	totalPages = (len(aliases) + aliasesPerPage - 1) / aliasesPerPage
	shownPage = clamp(page, 0, totalPages-1)
	start := shownPage * aliasesPerPage
	end := min(start+aliasesPerPage, len(aliases))

	var b strings.Builder
	if filter != "" {
		fmt.Fprintf(&b, "%d alias(es) for <b>%s</b> · page %d/%d:\n", len(aliases), escape(filter), shownPage+1, totalPages)
	} else {
		fmt.Fprintf(&b, "%d alias(es) · page %d/%d:\n", len(aliases), shownPage+1, totalPages)
	}
	for _, a := range aliases[start:end] {
		fmt.Fprintf(&b, "• <b>%s</b> → %s\n", escape(a.address), escape(strings.Join(a.recipients, ", ")))
	}

	if shownPage > 0 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("⬅️", fmt.Sprintf("page|%d", shownPage-1)))
	}
	nav = append(nav, tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d/%d", shownPage+1, totalPages), "noop"))
	if shownPage < totalPages-1 {
		nav = append(nav, tgbotapi.NewInlineKeyboardButtonData("➡️", fmt.Sprintf("page|%d", shownPage+1)))
	}
	return b.String(), nav, shownPage, totalPages
}

func renderEmptyAliases(bot *tgbotapi.BotAPI, chatID int64, messageID int, filter string) {
	text := "No aliases configured."
	if filter != "" {
		text = fmt.Sprintf("No aliases for <b>%s</b>.", escape(filter))
	}
	if messageID == 0 {
		sendText(bot, chatID, text)
		return
	}
	edit := tgbotapi.NewEditMessageText(chatID, messageID, text)
	edit.ParseMode = "HTML"
	if _, err := bot.Send(edit); err != nil {
		log.Printf("edit message: %v", err)
	}
}

func storeFilter(messageID int, filter string) {
	if len(listFilters) >= 1000 {
		listFilters = map[int]string{}
	}
	listFilters[messageID] = filter
}

// filterAliases keeps only aliases that forward to the given mailbox. The
// filter is either a local part (matches any domain) or a full email address.
func filterAliases(aliases []alias, filter string) []alias {
	filter = strings.ToLower(filter)
	var filtered []alias
	for _, a := range aliases {
		for _, r := range a.recipients {
			recipient := strings.ToLower(r)
			local := recipient
			if i := strings.Index(local, "@"); i != -1 {
				local = local[:i]
			}
			if recipient == filter || local == filter {
				filtered = append(filtered, a)
				break
			}
		}
	}
	return filtered
}

// parseAliases parses a postfix-virtual.cf database: one "<alias> <recipient>"
// pair per line, with optional comma-separated recipients and '#' comments.
// A "* " prefix (used by `setup alias list`) is tolerated.
func parseAliases(out string) []alias {
	var aliases []alias
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimSpace(strings.TrimPrefix(line, "* "))
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		a := alias{address: fields[0]}
		for _, r := range strings.Split(strings.Join(fields[1:], ","), ",") {
			if r = strings.TrimSpace(r); r != "" {
				a.recipients = append(a.recipients, r)
			}
		}
		aliases = append(aliases, a)
	}
	return aliases
}

func handleCallback(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) {
	if !authorized(cb.From) {
		return
	}
	bot.Request(tgbotapi.NewCallback(cb.ID, ""))
	if cb.Message == nil {
		return
	}

	parts := strings.Split(cb.Data, "|")
	switch parts[0] {
	case "noop":
	case "page":
		if len(parts) != 2 {
			return
		}
		page, err := strconv.Atoi(parts[1])
		if err != nil {
			return
		}
		renderAliases(bot, cb.Message.Chat.ID, cb.Message.MessageID, page, listFilters[cb.Message.MessageID])
	case "confirm_del":
		confirmDelete(bot, cb)
	case "cancel_del":
		delete(pendingDelete, cb.Message.MessageID)
		editText(bot, cb, "Cancelled.")
	}
}

func confirmDelete(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery) {
	req, ok := pendingDelete[cb.Message.MessageID]
	delete(pendingDelete, cb.Message.MessageID)
	if !ok {
		editText(bot, cb, "Nothing to delete.")
		return
	}

	if out, err := setup("alias", "del", req.aliasAddr, req.mailboxAddr); err != nil {
		editText(bot, cb, "Failed to delete <b>"+escape(req.aliasAddr)+"</b>:\n<code>"+escape(out)+"</code>")
		return
	}
	editText(bot, cb, "Deleted <b>"+escape(req.aliasAddr)+"</b> → <b>"+escape(req.mailboxAddr)+"</b>")
}

func clamp(n, lo, hi int) int {
	if n < lo {
		return lo
	}
	if n > hi {
		return hi
	}
	return n
}

func normalizeLocalPart(arg string) (string, error) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	if strings.Count(arg, "@") > 1 {
		return "", fmt.Errorf("invalid local part %q", arg)
	}
	if i := strings.Index(arg, "@"); i != -1 {
		arg = arg[:i]
	}
	if !localPartRe.MatchString(arg) {
		return "", fmt.Errorf("invalid local part %q", arg)
	}
	return arg, nil
}

// normalizeEmail validates and lowercases a full email address.
func normalizeEmail(arg string) (string, error) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	if strings.Count(arg, "@") != 1 {
		return "", fmt.Errorf("invalid email address %q", arg)
	}
	local, domain, _ := strings.Cut(arg, "@")
	if !localPartRe.MatchString(local) || !domainRe.MatchString(domain) {
		return "", fmt.Errorf("invalid email address %q", arg)
	}
	return arg, nil
}

// resolveMailbox turns a /alias_add or /alias_delete mailbox argument into a
// full address. A bare local part means a mailbox on MAIL_DOMAIN (local=true);
// a full address is used as-is, and local is true only when its domain is
// MAIL_DOMAIN, so callers can skip the existence check for external targets.
func resolveMailbox(arg string) (addr string, local bool, err error) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	if strings.Contains(arg, "@") {
		addr, err = normalizeEmail(arg)
		if err != nil {
			return "", false, err
		}
		_, domain, _ := strings.Cut(addr, "@")
		return addr, strings.EqualFold(domain, cfg.mailDomain), nil
	}
	localPart, err := normalizeLocalPart(arg)
	if err != nil {
		return "", false, err
	}
	return fmt.Sprintf("%s@%s", localPart, cfg.mailDomain), true, nil
}

// normalizeFilter validates a /alias_list mailbox filter, which is either a
// local part or a full email address.
func normalizeFilter(arg string) (string, error) {
	arg = strings.TrimSpace(strings.ToLower(arg))
	if strings.Contains(arg, "@") {
		return normalizeEmail(arg)
	}
	return normalizeLocalPart(arg)
}

func authorized(u *tgbotapi.User) bool {
	return u != nil && u.ID == cfg.userID
}

// logUnauthorized records an ignored message. Log output is throttled per user
// (user ID 0 covers messages with no sender) so a spam flood can't grow the
// container logs unboundedly, while a real misconfiguration stays diagnosable.
func logUnauthorized(u *tgbotapi.User) {
	var id int64
	if u != nil {
		id = u.ID
	}
	now := time.Now()
	if last, ok := lastUnauthorizedLog[id]; ok && now.Sub(last) < time.Minute {
		return
	}
	if len(lastUnauthorizedLog) >= 1000 {
		lastUnauthorizedLog = map[int64]time.Time{}
	}
	lastUnauthorizedLog[id] = now
	log.Printf("ignoring message from unauthorized user %d", id)
}

func sendText(bot *tgbotapi.BotAPI, chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "HTML"
	if _, err := bot.Send(msg); err != nil {
		log.Printf("send message: %v", err)
	}
}

func editText(bot *tgbotapi.BotAPI, cb *tgbotapi.CallbackQuery, text string) {
	msg := tgbotapi.NewEditMessageText(cb.Message.Chat.ID, cb.Message.MessageID, text)
	msg.ParseMode = "HTML"
	if _, err := bot.Send(msg); err != nil {
		log.Printf("edit message: %v", err)
	}
}

func escape(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"<", "&lt;",
		">", "&gt;",
		`"`, "&quot;",
	)
	return replacer.Replace(s)
}

func registerCommands(bot *tgbotapi.BotAPI) error {
	commands := []tgbotapi.BotCommand{
		{Command: "alias_list", Description: "List all aliases, optionally for one mailbox"},
		{Command: "alias_add", Description: "Add an alias: /alias_add <alias> <mailbox>"},
		{Command: "alias_delete", Description: "Delete an alias: /alias_delete <alias> <mailbox>"},
		{Command: "help", Description: "Show the available commands"},
	}
	_, err := bot.Request(tgbotapi.NewSetMyCommands(commands...))
	return err
}

func welcomeText() string {
	return "Welcome to your docker-mailserver alias management bot.\nUse /help to see the available commands."
}

func helpText() string {
	return fmt.Sprintf(`<b>%s</b> — manage docker-mailserver aliases.

Commands:
/alias_list [mailbox] — list aliases, optionally only those for one mailbox
/alias_add &lt;alias&gt; &lt;mailbox&gt; — create an alias → mailbox
/alias_delete &lt;alias&gt; &lt;mailbox&gt; — remove an alias → mailbox mapping (asks for confirmation)

&lt;mailbox&gt; is a local part (e.g. admin) or a full external address
(e.g. someone@external.example).

Examples:
/alias_add support admin     → support@%s → admin@%s
/alias_add support someone@external.example
/alias_delete support admin
`,
		appName,
		cfg.mailDomain,
		cfg.mailDomain,
	)
}
