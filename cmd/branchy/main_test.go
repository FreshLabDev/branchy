// SPDX-License-Identifier: Apache-2.0
package main

import (
	"context"
	"errors"
	"testing"
	"time"

	"branchy/internal/telegram"
)

func TestEnsureTelegramCommandsRetriesOnlyFailedScope(t *testing.T) {
	registrar := &fakeCommandRegistrar{failFirstGroup: true}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	ensureTelegramCommands(ctx, registrar, time.Millisecond)

	if registrar.privateCalls != 1 || registrar.groupCalls != 2 {
		t.Fatalf("private/group registrations = %d/%d, want 1/2", registrar.privateCalls, registrar.groupCalls)
	}
	if !registrar.groupEphemeral {
		t.Fatal("group /start must remain ephemeral on retry")
	}
}

type fakeCommandRegistrar struct {
	privateCalls   int
	groupCalls     int
	groupEphemeral bool
	failFirstGroup bool
}

func (f *fakeCommandRegistrar) SetMyCommandsForScope(_ context.Context, commands []telegram.BotCommand, scope *telegram.BotCommandScope) error {
	if scope == nil {
		return errors.New("missing scope")
	}
	switch scope.Type {
	case "all_private_chats":
		f.privateCalls++
	case "all_group_chats":
		f.groupCalls++
		if len(commands) == 1 {
			f.groupEphemeral = commands[0].IsEphemeral
		}
		if f.failFirstGroup && f.groupCalls == 1 {
			return errors.New("temporary failure")
		}
	default:
		return errors.New("unexpected scope")
	}
	return nil
}
