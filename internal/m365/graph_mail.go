package m365

// Mail through Microsoft Graph, read and write, as the person.

import (
	"context"
	"errors"
	"net/mail"
	"strings"
	"time"

	abs "github.com/microsoft/kiota-abstractions-go"
	"github.com/microsoftgraph/msgraph-sdk-go/models"
	"github.com/microsoftgraph/msgraph-sdk-go/users"

	"github.com/Embernet-ai/hearth/internal/connect"
)

var messageSelect = []string{"id", "conversationId", "subject", "from", "toRecipients", "ccRecipients", "bodyPreview",
	"receivedDateTime", "webLink", "isRead", "categories", "hasAttachments", "parentFolderId"}

func textHeaders() *abs.RequestHeaders {
	h := abs.NewRequestHeaders()
	h.Add("Prefer", `outlook.body-content-type="text"`)
	return h
}

func recipients(list []models.Recipientable) []string {
	var out []string
	for _, r := range list {
		if r.GetEmailAddress() != nil {
			out = append(out, s(r.GetEmailAddress().GetAddress()))
		}
	}
	return out
}

func messageOf(m models.Messageable, withBody bool) connect.Message {
	out := connect.Message{ID: s(m.GetId()), ThreadID: s(m.GetConversationId()), Subject: s(m.GetSubject()),
		Snippet: s(m.GetBodyPreview()), WebLink: s(m.GetWebLink()), To: recipients(m.GetToRecipients()),
		Cc: recipients(m.GetCcRecipients()), Labels: m.GetCategories()}
	if t := m.GetReceivedDateTime(); t != nil {
		out.Received = t.UTC()
	}
	if f := m.GetFrom(); f != nil && f.GetEmailAddress() != nil {
		out.From = s(f.GetEmailAddress().GetAddress())
	}
	if r := m.GetIsRead(); r != nil {
		out.Unread = !*r
	}
	if withBody && m.GetBody() != nil {
		out.Body = s(m.GetBody().GetContent())
		out.HTML = m.GetBody().GetContentType() != nil && *m.GetBody().GetContentType() == models.HTML_BODYTYPE
	}
	for _, a := range m.GetAttachments() {
		out.Attached = append(out.Attached, s(a.GetName()))
	}
	return out
}

func recipient(addr string) models.Recipientable {
	r := models.NewRecipient()
	e := models.NewEmailAddress()
	if a, err := mail.ParseAddress(addr); err == nil {
		e.SetAddress(ptr(a.Address))
		if a.Name != "" {
			e.SetName(ptr(a.Name))
		}
	} else {
		e.SetAddress(ptr(strings.TrimSpace(addr)))
	}
	r.SetEmailAddress(e)
	return r
}

func recipientList(addrs []string) []models.Recipientable {
	var out []models.Recipientable
	for _, a := range addrs {
		if strings.TrimSpace(a) != "" {
			out = append(out, recipient(a))
		}
	}
	return out
}

func outgoing(o connect.Outgoing) models.Messageable {
	m := models.NewMessage()
	m.SetSubject(ptr(o.Subject))
	b := models.NewItemBody()
	if o.HTML {
		b.SetContentType(ptr(models.HTML_BODYTYPE))
	} else {
		b.SetContentType(ptr(models.TEXT_BODYTYPE))
	}
	b.SetContent(ptr(o.Body))
	m.SetBody(b)
	m.SetToRecipients(recipientList(o.To))
	// Graph refuses a null recipient list, and the SDK writes an unset-but-empty slice as null, so each optional list
	// is only set when it has somebody in it.
	if cc := recipientList(o.Cc); len(cc) > 0 {
		m.SetCcRecipients(cc)
	}
	if bcc := recipientList(o.Bcc); len(bcc) > 0 {
		m.SetBccRecipients(bcc)
	}
	return m
}

// Messages lists the whole mailbox since a time, newest first, with bodies, for the sync that links mail to contacts.
func (x mailAdapter) Messages(ctx context.Context, since time.Time, limit int) ([]connect.Message, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	filter := "receivedDateTime ge " + since.UTC().Format("2006-01-02T15:04:05Z")
	cfg := &users.ItemMessagesRequestBuilderGetRequestConfiguration{
		Headers: textHeaders(),
		QueryParameters: &users.ItemMessagesRequestBuilderGetQueryParameters{
			Filter: &filter, Orderby: []string{"receivedDateTime desc"}, Top: ptr(int32(min(limit, 50))),
			Select: append(append([]string{}, messageSelect...), "body"),
		},
	}
	var out []connect.Message
	resp, err := x.client.Me().Messages().Get(ctx, cfg)
	for {
		if err != nil {
			return nil, x.fail(ctx, err)
		}
		for _, m := range resp.GetValue() {
			out = append(out, messageOf(m, true))
			if len(out) >= limit {
				return out, nil
			}
		}
		next := resp.GetOdataNextLink()
		if next == nil || *next == "" {
			return out, nil
		}
		resp, err = x.client.Me().Messages().WithUrl(*next).Get(ctx, cfg)
	}
}

var wellKnownFolder = map[string]string{"inbox": "inbox", "sent": "sentitems", "archive": "archive"}

// Inbox lists a page of the mailbox.
func (x mailAdapter) Inbox(ctx context.Context, q connect.MailQuery) ([]connect.Message, string, error) {
	limit := q.Limit
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	qp := &users.ItemMessagesRequestBuilderGetQueryParameters{Top: ptr(int32(limit)), Select: messageSelect}
	if strings.TrimSpace(q.Search) != "" {
		// Graph does not allow $orderby with $search.
		qp.Search = ptr(`"` + strings.ReplaceAll(strings.TrimSpace(q.Search), `"`, "") + `"`)
	} else {
		qp.Orderby = []string{"receivedDateTime desc"}
	}
	cfg := &users.ItemMessagesRequestBuilderGetRequestConfiguration{QueryParameters: qp}

	var resp models.MessageCollectionResponseable
	var err error
	switch {
	case q.Cursor != "":
		// A cursor is a next-link Graph gave. One that does not point at Graph is not one it gave.
		if !strings.HasPrefix(q.Cursor, x.s.graphBase) {
			return nil, "", errors.New("microsoft graph: that is not a page of this mailbox")
		}
		resp, err = x.client.Me().Messages().WithUrl(q.Cursor).Get(ctx, cfg)
	case strings.ToLower(q.Folder) == "all":
		resp, err = x.client.Me().Messages().Get(ctx, cfg)
	default:
		folder, ok := wellKnownFolder[strings.ToLower(q.Folder)]
		if !ok {
			folder = "inbox"
		}
		fcfg := &users.ItemMailFoldersItemMessagesRequestBuilderGetRequestConfiguration{
			QueryParameters: &users.ItemMailFoldersItemMessagesRequestBuilderGetQueryParameters{
				Top: qp.Top, Select: qp.Select, Orderby: qp.Orderby, Search: qp.Search}}
		resp, err = x.client.Me().MailFolders().ByMailFolderId(folder).Messages().Get(ctx, fcfg)
	}
	if err != nil {
		return nil, "", x.fail(ctx, err)
	}
	var out []connect.Message
	for _, m := range resp.GetValue() {
		out = append(out, messageOf(m, false))
	}
	next := ""
	if n := resp.GetOdataNextLink(); n != nil {
		next = *n
	}
	return out, next, nil
}

func (x mailAdapter) Get(ctx context.Context, id string) (connect.Message, error) {
	cfg := &users.ItemMessagesMessageItemRequestBuilderGetRequestConfiguration{
		Headers: textHeaders(),
		QueryParameters: &users.ItemMessagesMessageItemRequestBuilderGetQueryParameters{
			Select: append(append([]string{}, messageSelect...), "body"), Expand: []string{"attachments($select=name)"}},
	}
	m, err := x.client.Me().Messages().ByMessageId(id).Get(ctx, cfg)
	if err != nil {
		return connect.Message{}, x.fail(ctx, err)
	}
	return messageOf(m, true), nil
}

func (x mailAdapter) Send(ctx context.Context, o connect.Outgoing) error {
	body := users.NewItemSendMailPostRequestBody()
	body.SetMessage(outgoing(o))
	body.SetSaveToSentItems(ptr(true))
	return x.fail(ctx, x.client.Me().SendMail().Post(ctx, body, nil))
}

func (x mailAdapter) Reply(ctx context.Context, id string, o connect.Outgoing, all bool) error {
	if all {
		body := users.NewItemMessagesItemReplyAllPostRequestBody()
		body.SetComment(ptr(o.Body))
		return x.fail(ctx, x.client.Me().Messages().ByMessageId(id).ReplyAll().Post(ctx, body, nil))
	}
	body := users.NewItemMessagesItemReplyPostRequestBody()
	body.SetComment(ptr(o.Body))
	return x.fail(ctx, x.client.Me().Messages().ByMessageId(id).Reply().Post(ctx, body, nil))
}

func (x mailAdapter) Forward(ctx context.Context, id string, o connect.Outgoing) error {
	body := users.NewItemMessagesItemForwardPostRequestBody()
	body.SetComment(ptr(o.Body))
	body.SetToRecipients(recipientList(o.To))
	return x.fail(ctx, x.client.Me().Messages().ByMessageId(id).Forward().Post(ctx, body, nil))
}

func (x mailAdapter) Archive(ctx context.Context, id string) error {
	body := users.NewItemMessagesItemMovePostRequestBody()
	body.SetDestinationId(ptr("archive"))
	_, err := x.client.Me().Messages().ByMessageId(id).Move().Post(ctx, body, nil)
	return x.fail(ctx, err)
}

func (x mailAdapter) Label(ctx context.Context, id string, add, remove []string) error {
	cfg := &users.ItemMessagesMessageItemRequestBuilderGetRequestConfiguration{
		QueryParameters: &users.ItemMessagesMessageItemRequestBuilderGetQueryParameters{Select: []string{"categories"}}}
	m, err := x.client.Me().Messages().ByMessageId(id).Get(ctx, cfg)
	if err != nil {
		return x.fail(ctx, err)
	}
	drop := map[string]bool{}
	for _, r := range remove {
		drop[strings.ToLower(strings.TrimSpace(r))] = true
	}
	have := map[string]bool{}
	cats := []string{}
	for _, c := range append(append([]string{}, m.GetCategories()...), add...) {
		c = strings.TrimSpace(c)
		if c == "" || drop[strings.ToLower(c)] || have[strings.ToLower(c)] {
			continue
		}
		have[strings.ToLower(c)] = true
		cats = append(cats, c)
	}
	patch := models.NewMessage()
	patch.SetCategories(cats)
	_, err = x.client.Me().Messages().ByMessageId(id).Patch(ctx, patch, nil)
	return x.fail(ctx, err)
}

func (x mailAdapter) Labels(ctx context.Context) ([]string, error) {
	res, err := x.client.Me().Outlook().MasterCategories().Get(ctx, nil)
	if err != nil {
		return nil, x.fail(ctx, err)
	}
	var out []string
	for _, c := range res.GetValue() {
		out = append(out, s(c.GetDisplayName()))
	}
	return out, nil
}

func (x mailAdapter) MarkRead(ctx context.Context, id string, read bool) error {
	patch := models.NewMessage()
	patch.SetIsRead(ptr(read))
	_, err := x.client.Me().Messages().ByMessageId(id).Patch(ctx, patch, nil)
	return x.fail(ctx, err)
}

func (x mailAdapter) Delete(ctx context.Context, id string) error {
	return x.fail(ctx, x.client.Me().Messages().ByMessageId(id).Delete(ctx, nil))
}
