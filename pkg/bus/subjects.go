// Package bus provides the NATS connection management layer for Zester.
// It supports both embedded server mode (master) and client mode (peel),
// with JetStream enabled by default, KV bucket management, and MessagePack
// serialization for all payloads.
package bus

import "fmt"

// Subject prefixes for the Zester NATS hierarchy.
const (
	// SubjectPrefix is the root prefix for all Zester subjects.
	SubjectPrefix = "zester"

	// SubjectCmd is for master -> peel commands (request/reply).
	// Pattern: zester.cmd.<target>
	SubjectCmd = SubjectPrefix + ".cmd"

	// SubjectEvent is for peel -> master events (beacons, returns).
	// Pattern: zester.event.<peel-id>
	SubjectEvent = SubjectPrefix + ".event"

	// SubjectFact is for fact data publish/sync.
	// Pattern: zester.fact.<peel-id>
	SubjectFact = SubjectPrefix + ".fact"

	// SubjectBasket is for peel-to-peel data sharing.
	// Pattern: zester.basket.<key>
	SubjectBasket = SubjectPrefix + ".basket"

	// SubjectJob is for job tracking and returns.
	// Pattern: zester.job.<jid>
	SubjectJob = SubjectPrefix + ".job"

	// SubjectDispatch is used by the CLI to submit jobs to the master.
	// Pattern: zester.dispatch (request/reply)
	SubjectDispatch = SubjectPrefix + ".dispatch"

	// SubjectReactor is for event-driven reactions.
	SubjectReactor = SubjectPrefix + ".reactor"

	// SubjectReactorTest is for CLI -> master reactor rule dry-runs
	// (request/reply): given a match key and optional event data, the
	// masters return the matched rules and their rendered actions
	// WITHOUT executing them.
	SubjectReactorTest = SubjectReactor + ".test"

	// SubjectUpdateCmd is for master -> watchdog update commands (request/reply).
	// Pattern: zester.update.cmd.<id>
	SubjectUpdateCmd = SubjectPrefix + ".update.cmd"

	// SubjectUpdateRolloutStart is for CLI -> master rollout start (request/reply).
	SubjectUpdateRolloutStart = SubjectPrefix + ".update.rollout.start"

	// SubjectUpdateRolloutAbort is for CLI -> master rollout abort (request/reply).
	SubjectUpdateRolloutAbort = SubjectPrefix + ".update.rollout.abort"

	// SubjectTargetResolve is for CLI -> master target resolution (request/reply):
	// expands a target expression into the matching peel IDs.
	SubjectTargetResolve = SubjectPrefix + ".target.resolve"

	// SubjectAdminEnrollApprove is for CLI -> master enrollment approval (request/reply).
	SubjectAdminEnrollApprove = SubjectPrefix + ".admin.enroll.approve"

	// SubjectAdminEnrollReject is for CLI -> master enrollment rejection (request/reply).
	SubjectAdminEnrollReject = SubjectPrefix + ".admin.enroll.reject"

	// SubjectAdminEnrollRevoke is for CLI -> master enrollment revocation (request/reply).
	SubjectAdminEnrollRevoke = SubjectPrefix + ".admin.enroll.revoke"

	// SubjectAdminFileserverUpdate is for CLI -> master on-demand file
	// republish (`zester fileserver update`, request/reply). Every master
	// subscribes PLAIN (not queue-grouped), but only the publisher-lease
	// holder replies — non-holders stay silent so the requester never races
	// two replies; a no-responders timeout means no lease holder is up.
	SubjectAdminFileserverUpdate = SubjectPrefix + ".admin.fileserver.update"

	// SubjectAdminFileserverStatus identifies the publisher-lease holder
	// (`zester fileserver status`, request/reply, same holder-only-reply
	// contract as the update subject).
	SubjectAdminFileserverStatus = SubjectPrefix + ".admin.fileserver.status"
)

// Job sub-subjects appended to SubjectJob.<jid>.
const (
	// SubjectJobDispatch is published by the master with the job spec.
	SubjectJobDispatch = "dispatch"

	// SubjectJobAck is published by peels to acknowledge receipt.
	// Pattern: zester.job.<jid>.ack.<peel-id>
	SubjectJobAck = "ack"

	// SubjectJobReturn is published by peels with execution results.
	// Pattern: zester.job.<jid>.return.<peel-id>
	SubjectJobReturn = "return"

	// SubjectJobStatus is published by the master with aggregated status.
	SubjectJobStatus = "status"

	// SubjectJobCancel is a cancellation signal sent to peels.
	SubjectJobCancel = "cancel"

	// SubjectJobSchedule is published by peels when a locally scheduled
	// entry with return_job enabled completes. The trailing peel-id token
	// is enforced by the peel's NATS permissions, so a peel can only
	// report results as itself. Captured durably by the job-events stream.
	// Pattern: zester.job.<jid>.schedule.<peel-id>
	SubjectJobSchedule = "schedule"
)

// Beacon event subject suffix.
const (
	// SubjectBeacon is appended to event subjects for beacon data.
	// Pattern: zester.event.<peel-id>.beacon.<name>
	SubjectBeacon = "beacon"

	// SubjectEventSend is the event sub-token marking custom (non-beacon)
	// events published via the event.send module or the admin CLI.
	// Pattern: zester.event.<peel-id>.send.<dotted-tag>
	SubjectEventSend = "send"
)

// Trusted-origin tokens occupying the third position of event subjects
// (zester.event.<origin>....). Peel JWTs pin publishing to
// zester.event.<ownPeelID>.>, and enrollment rejects peel IDs with a
// leading underscore, so these origins can never be spoofed by a peel.
const (
	// OriginMaster marks master-synthesized events.
	// Pattern: zester.event._master.<dotted-tag>
	OriginMaster = "_master"

	// OriginAdmin marks operator-sent events (zester event send).
	// Pattern: zester.event._admin.send.<dotted-tag>
	OriginAdmin = "_admin"
)

// Wildcard tokens used in NATS subject matching.
const (
	// WildcardOne matches a single token in a subject.
	WildcardOne = "*"

	// WildcardMany matches one or more tokens at the tail of a subject.
	WildcardMany = ">"
)

// CmdSubject returns the command subject for a specific target.
// Example: CmdSubject("web-01") -> "zester.cmd.web-01"
func CmdSubject(target string) string {
	return SubjectCmd + "." + target
}

// CmdSubjectAll returns the wildcard subject for all commands.
// Returns: "zester.cmd.*"
func CmdSubjectAll() string {
	return SubjectCmd + "." + WildcardOne
}

// EventSubject returns the event subject for a specific peel.
// Example: EventSubject("web-01") -> "zester.event.web-01"
func EventSubject(peelID string) string {
	return SubjectEvent + "." + peelID
}

// EventSubjectAll returns the wildcard subject for all peel events.
// Returns: "zester.event.>"
func EventSubjectAll() string {
	return SubjectEvent + "." + WildcardMany
}

// BeaconSubject returns the beacon event subject for a specific peel and beacon name.
// Example: BeaconSubject("web-01", "disk") -> "zester.event.web-01.beacon.disk"
func BeaconSubject(peelID, name string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectEvent, peelID, SubjectBeacon, name)
}

// PeelEventSendSubject returns the custom-event subject for a peel and a
// DOTTED tag (slash tags map 1:1 to dotted tags — see pkg/event.DottedTag).
// The peel-id token is enforced by the peel's NATS publish permissions, so
// a peel can only emit events as itself.
// Example: PeelEventSendSubject("web-01", "myco.deploy.finished") ->
// "zester.event.web-01.send.myco.deploy.finished"
func PeelEventSendSubject(peelID, dottedTag string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectEvent, peelID, SubjectEventSend, dottedTag)
}

// MasterEventSubject returns the subject for a master-synthesized event
// with the given DOTTED tag. Only masters (zester.> grant) can publish
// under the _master origin.
// Example: MasterEventSubject("enroll.pending.enr-1") ->
// "zester.event._master.enroll.pending.enr-1"
func MasterEventSubject(dottedTag string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectEvent, OriginMaster, dottedTag)
}

// AdminEventSendSubject returns the subject for an operator-sent event with
// the given DOTTED tag (the zester event send CLI). Requires the admin
// JWT's zester.event._admin.> publish grant.
// Example: AdminEventSendSubject("myco.deploy.finished") ->
// "zester.event._admin.send.myco.deploy.finished"
func AdminEventSendSubject(dottedTag string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectEvent, OriginAdmin, SubjectEventSend, dottedTag)
}

// FactSubject returns the fact subject for a specific peel.
// Example: FactSubject("web-01") -> "zester.fact.web-01"
func FactSubject(peelID string) string {
	return SubjectFact + "." + peelID
}

// BasketSubject returns the basket subject for a peel and function.
// Example: BasketSubject("web-01", "network.ip_addrs") -> "zester.basket.web-01.network.ip_addrs"
func BasketSubject(peelID, function string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectBasket, peelID, function)
}

// JobSubject returns the base job subject for a job ID.
// Example: JobSubject("abc123") -> "zester.job.abc123"
func JobSubject(jid string) string {
	return SubjectJob + "." + jid
}

// JobDispatchSubject returns the dispatch subject for a job.
// Example: JobDispatchSubject("abc123") -> "zester.job.abc123.dispatch"
func JobDispatchSubject(jid string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectJob, jid, SubjectJobDispatch)
}

// JobAckSubject returns the ack subject for a peel acknowledging a job.
// Example: JobAckSubject("abc123", "web-01") -> "zester.job.abc123.ack.web-01"
func JobAckSubject(jid, peelID string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectJob, jid, SubjectJobAck, peelID)
}

// JobReturnSubject returns the return subject for a peel's job result.
// Example: JobReturnSubject("abc123", "web-01") -> "zester.job.abc123.return.web-01"
func JobReturnSubject(jid, peelID string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectJob, jid, SubjectJobReturn, peelID)
}

// JobStatusSubject returns the status subject for job aggregation.
// Example: JobStatusSubject("abc123") -> "zester.job.abc123.status"
func JobStatusSubject(jid string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectJob, jid, SubjectJobStatus)
}

// JobCancelSubject returns the cancel subject for a job.
// Example: JobCancelSubject("abc123") -> "zester.job.abc123.cancel"
func JobCancelSubject(jid string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectJob, jid, SubjectJobCancel)
}

// JobScheduleSubject returns the scheduled-result subject for a peel's
// synthetic scheduler job.
// Example: JobScheduleSubject("abc123", "web-01") -> "zester.job.abc123.schedule.web-01"
func JobScheduleSubject(jid, peelID string) string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectJob, jid, SubjectJobSchedule, peelID)
}

// JobScheduleWildcard returns the filter subject matching scheduled-result
// messages from all peels for all jobs.
// Returns: "zester.job.*.schedule.*"
func JobScheduleWildcard() string {
	return fmt.Sprintf("%s.%s.%s.%s", SubjectJob, WildcardOne, SubjectJobSchedule, WildcardOne)
}

// JobAllSubject returns the wildcard for all events on a specific job.
// Example: JobAllSubject("abc123") -> "zester.job.abc123.>"
func JobAllSubject(jid string) string {
	return fmt.Sprintf("%s.%s.%s", SubjectJob, jid, WildcardMany)
}

// UpdateCmdSubject returns the update command subject for a specific node.
// Example: UpdateCmdSubject("web-01") -> "zester.update.cmd.web-01"
func UpdateCmdSubject(nodeID string) string {
	return SubjectUpdateCmd + "." + nodeID
}

// UpdateCmdSubjectAll returns the wildcard subject for all update commands.
// Returns: "zester.update.cmd.*"
func UpdateCmdSubjectAll() string {
	return SubjectUpdateCmd + "." + WildcardOne
}
