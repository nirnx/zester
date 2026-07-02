package exec

// CronEntry represents a single crontab entry.
type CronEntry struct {
	Minute     string
	Hour       string
	DayOfMonth string
	Month      string
	DayOfWeek  string
	Command    string
	User       string
	Comment    string
}
