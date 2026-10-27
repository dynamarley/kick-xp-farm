package dashboard

import "fmt"

func (m *model) dailyRewardLabel() string {
	if m.opts.Config.DisableDailyRewards || m.dailyReward.State == "disabled" {
		return "Auto-claim off"
	}
	switch m.dailyReward.State {
	case "in_progress":
		return fmt.Sprintf("%.0f / %.0f min · auto-claim on", m.dailyReward.Progress, m.dailyReward.Required)
	case "claimed":
		return "Collected · waiting for reset"
	case "claimable":
		return "Ready · collecting automatically"
	case "none":
		return "No daily watch reward available"
	case "unavailable":
		return "Unavailable · retrying later"
	default:
		if !m.running {
			return "Auto-claim on · bot paused"
		}
		return "Checking daily rewards..."
	}
}
