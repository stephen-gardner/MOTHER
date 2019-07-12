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

func main() {

	if err := openConnection("./mother.db", "CKL5EHAH0"); err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	mom := newMother("",
		"test_server", "CKL5EHAH0")

	for msg := range mom.rtm.IncomingEvents {
		switch ev := msg.Data.(type) {

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

		default:

			// Ignore other events..
			// fmt.Printf("Unexpected: %v\n", msg.Data)
		}
	}
}
