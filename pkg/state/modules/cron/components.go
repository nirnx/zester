package cronmod

// components.go — the cron.* FAMILY PARAMETER COMPONENTS (keystone spec
// §13). Strictly scoped to cron.*; see modules/file/components.go for the model. The
// primary (`name`, the identifier comment) stays member-declared.

// cronTargetParam identifies the managed crontab entry: whose crontab, and
// the entry's command line (the Salt-identifier pair every member needs).
type cronTargetParam struct {
	User    string `zester:"user,default=root" usage:"user whose crontab is managed; defaults to root"`
	Command string `zester:"command,required" usage:"command line of the managed crontab entry; required"`
}
