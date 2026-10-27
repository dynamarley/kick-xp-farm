package kick

import "time"

type State string

const (
	Discovering State = "discovering"
	Connecting  State = "connecting"
	Watching    State = "watching"
	Waiting     State = "waiting"
)

// Event exposes connection state without credentials or raw server messages.
type Event struct {
	Kind     string
	State    State
	Channel  string
	Channels []LiveChannel
	Followed []FollowedChannel
	Reward   *DailyRewardStatus
	Message  string
	At       time.Time
}

func (c *Client) WithEvents(observer func(Event)) *Client {
	c.observer = observer
	return c
}

func (c *Client) emit(event Event) {
	if c.observer != nil {
		c.eventMu.Lock()
		defer c.eventMu.Unlock()
		event.At = time.Now()
		c.observer(event)
	}
}
