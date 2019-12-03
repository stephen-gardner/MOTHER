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
	parent := ctx.mom.getMsg("sessionNotice", []langVar{
		{"USERS", strings.Join(tagged, ", ")},
	})
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

func findPreviousConv(ctx *convInitContext) {
	if ctx.err != nil {
		return
	}
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
	ctx.msg = append(ctx.msg, ctx.mom.getMsg("sessionStartConv", []langVar{
		{"THREAD_ID", ctx.conv.ThreadID},
	}))
	if !ctx.resumed && !ctx.switched {
		ctx.conv.sendMessageToDM(ctx.mom.getMsg("sessionStartDirect", nil))
		if ctx.prev != nil {
			ctx.msg = append(ctx.msg, ctx.mom.getMsg("sessionStartPrev", []langVar{
				{"THREAD_LINK", ctx.mom.getMessageLink(ctx.prev.ThreadID)},
			}))
		}
	}
}

func resumeNotice(ctx *convInitContext) {
	if ctx.newThread {
		// For conversations resumed with a command
		ctx.prev.sendMessageToThread(ctx.mom.getMsg("sessionResumeTo", []langVar{
			{"THREAD_LINK", ctx.mom.getMessageLink(ctx.conv.ThreadID)},
		}))
		ctx.msg = append(ctx.msg, ctx.mom.getMsg("sessionResumeFrom", []langVar{
			{"THREAD_LINK", ctx.mom.getMessageLink(ctx.prev.ThreadID)},
		}))
	}
	if !ctx.switched {
		ctx.conv.sendMessageToDM(ctx.mom.getMsg("sessionResumeDirect", nil))
		// For conversations resumed with a message
		if !ctx.newThread {
			ctx.msg = append(ctx.msg, ctx.mom.getMsg("sessionResumeConv", nil))
		}
	}
}

func switchContext(ctx *convInitContext) {
	// Remove previous context from tracked conversations
	for i, prev := range ctx.mom.Conversations {
		if prev.ThreadID != ctx.prev.ThreadID {
			continue
		}
		ctx.mom.Conversations = append(ctx.mom.Conversations[:i], ctx.mom.Conversations[i+1:]...)
		if err := ctx.prev.setActive(false); err != nil {
			ctx.mom.log.Println(err)
		}
		break
	}
	if (ctx.resumed && !ctx.newThread) || !ctx.resumed {
		ctx.prev.sendMessageToThread(ctx.mom.getMsg("sessionContextSwitchedTo", []langVar{
			{"THREAD_LINK", ctx.mom.getMessageLink(ctx.conv.ThreadID)},
		}))
		ctx.msg = append(ctx.msg, ctx.mom.getMsg("sessionContextSwitchedFrom", []langVar{
			{"THREAD_LINK", ctx.mom.getMessageLink(ctx.prev.ThreadID)},
		}))
	}
}

func (ctx *convInitContext) create() (*Conversation, error) {
	if findPreviousConv(ctx); ctx.err == nil {
		ctx.err = db.
			Model(ctx.mom).
			Association("Conversations").
			Append(ctx.conv).Error
	}
	if ctx.err != nil {
		if ctx.newThread {
			ctx.conv.abandon()
		}
		return nil, ctx.err
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
