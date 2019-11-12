package main

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/jinzhu/gorm"
	"github.com/nlopes/slack"
)

type convInitContext struct {
	mom       *Mother
	conv      *Conversation
	prev      *Conversation
	msg       []string
	newThread bool
	resumed   bool
	switched  bool
	err       error
}

var ErrUserNotAllowed = errors.New("user not allowed")

func (mom *Mother) newConversation() *convInitContext {
	return &convInitContext{
		mom: mom,
		msg: make([]string, 0),
	}
}

func (ctx *convInitContext) postNewThread(directID string, slackIDs []string) *convInitContext {
	if ctx.err != nil {
		return ctx
	}
	if ctx.resumed {
		directID = ctx.conv.DirectID
		slackIDs = strings.Split(ctx.conv.SlackIDs, ",")
	} else {
		sort.Strings(slackIDs)
	}
	tagged := make([]string, 0)
	for _, ID := range slackIDs {
		tagged = append(tagged, fmt.Sprintf("<@%s>", ID))
	}
	parent := fmt.Sprintf(ctx.mom.getMsg("sessionNotice"), strings.Join(tagged, ", "))
	threadID, err := ctx.mom.postMessage(ctx.mom.config.ChanID, "", parent)
	if err != nil {
		ctx.err = err
		return ctx
	}
	ctx.conv = &Conversation{
		MotherID: ctx.mom.ID,
		SlackIDs: strings.Join(slackIDs, ","),
		DirectID: directID,
		ThreadID: threadID,
	}
	ctx.conv.init(ctx.mom)
	ctx.newThread = true
	return ctx
}

func (ctx *convInitContext) loadConversation(threadID string) *convInitContext {
	if ctx.err != nil {
		return ctx
	}
	conv := &Conversation{}
	err := db.
		Where("mother_id = ? AND thread_id = ?", ctx.mom.ID, threadID).
		Preload("MessageLogs").
		First(conv).Error
	if err != nil {
		ctx.err = err
		return ctx
	}
	ctx.conv = conv
	for _, slackID := range strings.Split(conv.SlackIDs, ",") {
		// Prevent reactivating conversations with channel members or blacklisted users
		if ctx.mom.hasMember(slackID) || ctx.mom.isBlacklisted(slackID) {
			ctx.err = ErrUserNotAllowed
			return ctx
		}
	}
	if _, _, _, err := ctx.mom.rtm.OpenConversation(
		&slack.OpenConversationParameters{ChannelID: conv.DirectID},
	); err != nil {
		ctx.err = err
		return ctx
	}
	conv.init(ctx.mom)
	conv.update()
	ctx.resumed = true
	return ctx
}

func abandonConversation(ctx *convInitContext) {
	if _, _, err := ctx.mom.rtm.DeleteMessage(ctx.mom.config.ChanID, ctx.conv.ThreadID); err != nil {
		// In the worst case, this could result in an ugly situation where channel members are unknowingly sending
		// messages to an inactive thread, but the chances of this many things suddenly going wrong is extremely
		// unlikely
		ctx.mom.log.Println(err)
	}
}

func findPreviousConv(ctx *convInitContext) {
	for _, prev := range ctx.mom.Conversations {
		if prev.Active && prev.DirectID == ctx.conv.DirectID {
			ctx.prev = &prev
			ctx.switched = true
			return
		}
	}
	prev := &Conversation{mom: ctx.mom}
	err := db.
		Where("mother_id = ? AND slack_ids = ?", ctx.mom.ID, ctx.conv.SlackIDs).
		Order("updated_at desc, id desc").
		First(prev).Error
	if err != nil {
		if err != gorm.ErrRecordNotFound {
			ctx.err = err
		}
		return
	}
	ctx.prev = prev
}

func newThreadNotice(ctx *convInitContext) {
	ctx.msg = append(ctx.msg, fmt.Sprintf(ctx.mom.getMsg("sessionStartConv"), ctx.conv.ThreadID))
	if !ctx.resumed && !ctx.switched {
		ctx.conv.sendMessageToDM(ctx.mom.getMsg("sessionStartDirect"))
		if ctx.prev != nil {
			link := ctx.mom.getMessageLink(ctx.prev.ThreadID)
			ctx.msg = append(ctx.msg, fmt.Sprintf(ctx.mom.getMsg("sessionStartPrev"), link))
		}
	}
}

func resumeNotice(ctx *convInitContext) {
	if ctx.newThread {
		// For conversations resumed with a command
		link := ctx.mom.getMessageLink(ctx.conv.ThreadID)
		ctx.prev.sendMessageToThread(fmt.Sprintf(ctx.mom.getMsg("sessionResumeTo"), link))
		link = ctx.mom.getMessageLink(ctx.prev.ThreadID)
		ctx.msg = append(ctx.msg, fmt.Sprintf(ctx.mom.getMsg("sessionResumeFrom"), link))
	}
	if !ctx.switched {
		ctx.conv.sendMessageToDM(ctx.mom.getMsg("sessionResumeDirect"))
		// For conversations resumed with a message
		if !ctx.newThread {
			ctx.msg = append(ctx.msg, ctx.mom.getMsg("sessionResumeConv"))
		}
	}
}

func switchContext(ctx *convInitContext) {
	// Remove previous context from tracked conversations
	for i, prev := range ctx.mom.Conversations {
		if prev.Active && prev.ThreadID == ctx.prev.ThreadID {
			ctx.mom.Conversations = append(ctx.mom.Conversations[:i], ctx.mom.Conversations[i+1:]...)
			if err := ctx.prev.setActive(false); err != nil {
				ctx.mom.log.Println(err)
			}
			break
		}
	}
	if (ctx.resumed && !ctx.newThread) || !ctx.resumed {
		link := ctx.mom.getMessageLink(ctx.conv.ThreadID)
		ctx.prev.sendMessageToThread(fmt.Sprintf(ctx.mom.getMsg("sessionContextSwitchedTo"), link))
		link = ctx.mom.getMessageLink(ctx.prev.ThreadID)
		ctx.msg = append(ctx.msg, fmt.Sprintf(ctx.mom.getMsg("sessionContextSwitchedFrom"), link))
	}
}

func (ctx *convInitContext) create() (*Conversation, error) {
	if ctx.err == nil {
		findPreviousConv(ctx)
	}
	if ctx.err != nil {
		if ctx.newThread {
			abandonConversation(ctx)
		}
		return nil, ctx.err
	}
	err := db.
		Model(ctx.mom).
		Association("Conversations").
		Append(ctx.conv).Error
	if err != nil {
		if ctx.newThread {
			abandonConversation(ctx)
		}
		return nil, err
	}
	if ctx.newThread {
		newThreadNotice(ctx)
	}
	if ctx.resumed {
		resumeNotice(ctx)
	}
	if ctx.switched {
		switchContext(ctx)
	}
	if _, err := ctx.conv.postMessageToThread(strings.Join(ctx.msg, "\n")); err != nil {
		ctx.mom.log.Println(err)
	}
	return ctx.conv, nil
}
