package main

import (
	"fmt"
	"github.com/nlopes/slack"
	"log"
	"time"
)

const (
	blacklistedUser            = ">_*User <@%s> can not start conversations.*_"
	highVolumeError            = ">_*We are currently experiencing an unusually high volume of requests. Please try again in a moment.*_"
	inConvChannel              = ">_*Users in `#%s` can not start conversations. Send `!help` for a list of available commands.*_"
	listBlacklisted            = "*Blacklisted users:* %s"
	msgCopyFmt                 = "*<@%s>:* %s"
	reactFailure               = "x"
	reactSuccess               = "white_check_mark"
	reactUnknown               = "question"
	sessionContextSwitchedFrom = ">_*Session context switched from [%s].*_"
	sessionContextSwitchedTo   = ">_*Session context switched to [%s].*_"
	sessionExpiredConv         = ">_*Session [%s] has expired.*_\n>Edits/reactions to previous messages will no longer be reflected in communications."
	sessionExpiredDirect       = ">_*Session has expired.*_\n>If your issue has not yet been resolved, an RA will be contacting you ASAP.\n>Edits/reactions to previous messages will no longer be reflected in communications."
	sessionNotice              = "_*Conversation started with: %s*_\n_(converse in thread under this message)_"
	sessionResumeConv          = ">_*Session resumed.*_"
	sessionResumeDirect        = ">_*An RA has resumed your session.*_"
	sessionStart               = ">_*A dialogue has been started with the RA team. An RA will reach out to you shortly.*_"
)

type botConfig struct {
	name   string
	token  string
	chanID string
}

type botEvent struct {
	mom *mother
	msg slack.RTMEvent
}

type scrubEvent struct {
	Type string
}

func handleEvents(events chan botEvent) {
	for bot := range events {
		mom := bot.mom
		if !mom.online {
			continue
		}

		switch ev := bot.msg.Data.(type) {
		case *slack.MessageEvent:
			if ev.User == mom.rtm.GetInfo().User.ID || mom.isBlacklisted(ev.User) || len(ev.Text) == 0 {
				break
			}
			chanInfo := mom.getChannelInfo(ev.Channel)
			if chanInfo == nil {
				break
			}

			if ev.SubType == "message_changed" && ev.SubMessage.User != mom.rtm.GetInfo().User.ID {
				mom.handleMessageChangedEvent(ev, chanInfo)
			} else if ev.Channel == mom.chanID {
				mom.handleChannelMessageEvent(ev)
			} else if chanInfo.IsIM || chanInfo.IsMpIM {
				mom.handleDirectMessageEvent(ev, chanInfo)
			}

		case *slack.UserTypingEvent:
			mom.handleUserTypingEvent(ev)

		case *slack.ReactionAddedEvent:
			mom.handleReactionAddedEvent(ev)

		case *slack.ReactionRemovedEvent:
			mom.handleReactionRemovedEvent(ev)

		case *slack.ConnectedEvent:
			fmt.Println("Infos:", ev.Info)
			fmt.Println("Connection counter:", ev.ConnectionCount)
			mom.rtm.SendMessage(mom.rtm.NewOutgoingMessage("Hello world", mom.chanID))

		case *slack.InvalidAuthEvent:
			fmt.Printf("Invalid credentials")
			return

		case *slack.RateLimitedError:
			log.Println("Hitting RTM rate limit")
			time.Sleep(ev.RetryAfter * time.Second)

		case *scrubEvent:
			mom.reapConversations(30)

		default:
			// Ignore other events..
		}
	}
}

func main() {
	if err := openConnection("./mother.db"); err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	bc := []botConfig{
		{
			name:   "MOTHER",
			token:  "",
			chanID: "CKL5EHAH0",
		},
		{
			name:   "Percy",
			token:  "",
			chanID: "GL73S064R",
		},
	}

	events := make(chan botEvent)
	defer close(events)

	mothers := make([]*mother, len(bc))
	for i, config := range bc {
		mothers[i] = newMother(config.token, config.name, config.chanID)

		go func(mom *mother) {
			for msg := range mom.rtm.IncomingEvents {
				events <- botEvent{
					mom: mom,
					msg: msg,
				}
			}
		}(mothers[i])

		go func(mom *mother) {
			for {
				time.Sleep(time.Minute)
				events <- botEvent{
					mom: mom,
					msg: slack.RTMEvent{
						Type: "scrub",
						Data: &scrubEvent{"scrub"},
					},
				}
			}
		}(mothers[i])
	}

	handleEvents(events)
}
