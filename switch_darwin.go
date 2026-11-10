package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"time"
)

// liveSwitchEnv is the real Claude on this Mac, as a switch sees it.
func liveSwitchEnv(s *switcher) switchEnv {
	sleep := func(ctx context.Context, wait time.Duration) error {
		timer := time.NewTimer(wait)
		defer timer.Stop()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
			return nil
		}
	}
	return switchEnv{
		desktop:   claudeDesktop,
		decrypt:   decryptValue,
		selfCheck: func(ctx context.Context) selfCheck { return runSelfCheck(ctx, liveSelfCheckEnv()) },
		close: func(ctx context.Context, onWait func([]string)) error {
			return closeClaudeAndWait(ctx, liveCloseEnv(onWait))
		},
		closed: func(context.Context) bool { return closedNow(processTable, claudeDesktop().dir) },
		start:  func() error { return startDesktop("") },
		identify: func(ctx context.Context, account string) (string, error) {
			token, err := s.tokenFor(account, account)
			if err != nil {
				return "", err
			}
			ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			p, err := fetchProfile(ctx, token)
			if err != nil {
				return "", err
			}
			if !idPattern.MatchString(p.Account.UUID) {
				return "", errors.New("the service did not name an account")
			}
			return p.Account.UUID, nil
		},
		sleep:         sleep,
		now:           time.Now,
		claudeVersion: currentDesktopVersion,
		claudeDir: func() string {
			home, _ := os.UserHomeDir()
			return filepath.Join(home, ".claude")
		},
		verifyPoll:     2 * time.Second,
		verifyLimit:    90 * time.Second,
		signedOutGrace: 6 * time.Second,
		verifySettle:   10 * time.Second,
	}
}
