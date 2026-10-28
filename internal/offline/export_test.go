package offline

var (
	VerifyPassword = (*Env).verifyPassword
	Begin          = (*Env).begin
	OpenStore      = (*Env).openStore
	NewService     = (*Env).newService
	CrashPoint     = crashPoint
)

const (
	RefuseJournals    = refuseJournals
	AllowSwapJournal  = allowSwapJournal
	AllowScrubJournal = allowScrubJournal
)
