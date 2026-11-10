package main

// transactionOps keeps the ordered safety boundary testable without launching
// Claude or reading any real sign-in. Only the desktop adapter supplies these.
type transactionOps struct {
	Prepare func() error
	Quit    func() error
	Closed  func() bool
	Backup  func() (string, error)
	Active  func() (string, error)
	Seal    func(string) error
	Restore func(string) error
	Start   func() error
}

// runDesktopTransaction never discards the cause of a failure. Every error it
// returns carries a code and a sentence that states the outcome and the cause
// (see errcode.go: the cause is described, never printed raw).
func runDesktopTransaction(op transactionOps, change func(string) error) (string, error) {
	if err := op.Prepare(); err != nil {
		return "", withCode("PREPARE_FAILED", err)
	}
	if err := op.Quit(); err != nil {
		return "", withCode("CLAUDE_STILL_OPEN", err)
	}
	if !op.Closed() {
		return "", coded("CLAUDE_STILL_OPEN", "Claude is still open, so nothing was changed.")
	}
	bk, err := op.Backup()
	if err != nil {
		_ = op.Start()
		return "", codedf("BACKUP_FAILED", "A safety backup could not be made, so nothing was changed (%w).", err)
	}
	active, err := op.Active()
	if err != nil {
		err = codedf("IDENTITY_UNAVAILABLE", "the account Claude is signed in to could not be read (%w)", err)
	}
	if !op.Closed() {
		return bk, coded("CLAUDE_REOPENED", "Claude reopened before anything was changed. Nothing was switched.")
	}
	if err == nil {
		err = change(active)
	}
	if !op.Closed() {
		if err != nil {
			return bk, codedf("CLAUDE_REOPENED", "Claude reopened during the switch, so it was stopped without overwriting live files (%w).", err)
		}
		return bk, coded("CLAUDE_REOPENED", "Claude reopened during the switch, so it was stopped without overwriting live files.")
	}
	sealErr := op.Seal(bk)
	if err != nil || sealErr != nil {
		cause := err
		if cause == nil {
			cause = codedf("SEAL_FAILED", "the backup record could not be finished (%w)", sealErr)
		}
		cause = withCode("ROLLED_BACK", cause)
		if !op.Closed() {
			return bk, codedf("CLAUDE_REOPENED", "Claude reopened, so the switch was stopped without overwriting live files (%w).", cause)
		}
		if restoreErr := op.Restore(bk); restoreErr != nil {
			return bk, codedf("RECOVERY_NEEDED", "The switch stopped and could not be undone automatically (%w). Use Fix an interrupted switch before opening Claude.", cause)
		}
		if startErr := op.Start(); startErr != nil {
			return bk, codedf("RESTORED_OPEN_CLAUDE", "The switch did not happen and everything was put back (%w). Open Claude yourself to continue.", cause)
		}
		// The cause's own code is the reported code.
		return bk, codedf(errorCode(cause), "The switch did not happen and everything was put back: %w.", cause)
	}
	if startErr := op.Start(); startErr != nil {
		// A launcher can fail after creating a process. Never restore files
		// underneath a live Claude, even when the launcher reported failure.
		if !op.Closed() {
			return bk, codedf("LAUNCH_UNCERTAIN", "Claude may not have started properly after the switch (%w).", startErr)
		}
		if restoreErr := op.Restore(bk); restoreErr != nil {
			return bk, codedf("RECOVERY_NEEDED", "Claude could not be started and the switch could not be undone automatically (%w). Use Fix an interrupted switch.", startErr)
		}
		if again := op.Start(); again != nil {
			return bk, codedf("RESTORED_OPEN_CLAUDE", "Claude could not be started; everything was put back (%w). Open Claude yourself to continue.", startErr)
		}
		return bk, codedf("START_FAILED", "Claude could not start on the new account, so everything was put back (%w).", startErr)
	}
	return bk, nil
}
