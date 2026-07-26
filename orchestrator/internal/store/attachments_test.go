package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

func pgAttachmentFixture(t *testing.T) (*PGStore, domain.Run, string, string) {
	t.Helper()
	ctx := context.Background()
	st, runID := pgTestStore(t)
	run, err := st.GetRun(ctx, runID)
	if err != nil {
		t.Fatal(err)
	}
	prefix := "run-inputs/pg-test-" + domain.NewID() + "/"
	user := &domain.User{ID: domain.NewID(), DisplayName: "Attachment Test", CreatedAt: time.Now().UTC()}
	identity := &domain.UserIdentity{
		ID: domain.NewID(), Provider: domain.ProviderGitea,
		ProviderUID: "attachment-" + user.ID, Username: "attachment-" + user.ID,
		AccessTokenEnc: []byte("test-ciphertext"), CreatedAt: time.Now().UTC(),
	}
	if _, err := st.CreateUserWithIdentity(ctx, user, identity); err != nil {
		t.Fatal(err)
	}
	userID := user.ID
	// Attachment bindings and stages intentionally have no cascading FK because
	// object GC must survive Run/Project deletion. Keep shared PG test databases
	// clean by deleting only this test's opaque-key namespace.
	t.Cleanup(func() {
		_, _ = st.Pool().Exec(ctx, `DELETE FROM run_attachment_bindings WHERE object_key LIKE $1`, prefix+"%")
		_, _ = st.Pool().Exec(ctx, `DELETE FROM run_attachment_stages WHERE object_key LIKE $1`, prefix+"%")
		_, _ = st.Pool().Exec(ctx, `UPDATE runs SET triggered_by_user_id=NULL WHERE triggered_by_user_id=$1`, userID)
		_, _ = st.Pool().Exec(ctx, `DELETE FROM users WHERE id=$1`, userID)
	})
	return st, *run, userID, prefix
}

func createPGAttachmentStage(t *testing.T, st *PGStore, projectID, userID, id, objectKey string, size int64, uploaded bool) *domain.AttachmentStage {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC()
	stage := &domain.AttachmentStage{
		ID: id, ProjectID: projectID, CreatedBy: userID, ObjectKey: objectKey,
		DisplayName: id + ".txt", ContentType: "text/plain", SizeBytes: size,
		CreatedAt: now, ExpiresAt: now.Add(time.Hour),
	}
	if err := st.CreateAttachmentStage(ctx, stage); err != nil {
		t.Fatal(err)
	}
	if uploaded {
		if _, err := st.ClaimAttachmentStageUpload(ctx, stage.ID, projectID, userID, now); err != nil {
			t.Fatal(err)
		}
		if err := st.MarkAttachmentStageUploaded(ctx, stage.ID, size, now); err != nil {
			t.Fatal(err)
		}
	}
	return stage
}

func TestMemAttachmentStagesAreConsumedAtomically(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now().UTC()
	user := "u1"
	ready := &domain.AttachmentStage{ID: "ready", ProjectID: "p", CreatedBy: user, ObjectKey: "run-inputs/p/ready", DisplayName: "a.txt", SizeBytes: 3, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	pending := &domain.AttachmentStage{ID: "pending", ProjectID: "p", CreatedBy: user, ObjectKey: "run-inputs/p/pending", DisplayName: "b.txt", SizeBytes: 4, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateAttachmentStage(ctx, ready); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateAttachmentStage(ctx, pending); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, ready.ID, "p", user, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachmentStageUploaded(ctx, ready.ID, 3, now); err != nil {
		t.Fatal(err)
	}
	run := &domain.Run{ID: "r1", ProjectID: "p", ServiceID: "s", Prompt: "x", Status: domain.StatusQueued, CreatedAt: now, TriggeredByUserID: &user, AttachmentStageIDs: []string{ready.ID, pending.ID}}
	if err := st.CreateRun(ctx, run); err == nil {
		t.Fatal("unuploaded stage must reject whole run")
	}
	if _, err := st.GetAttachmentStage(ctx, ready.ID); err != nil {
		t.Fatalf("atomic rejection consumed ready stage: %v", err)
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, pending.ID, "p", user, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachmentStageUploaded(ctx, pending.ID, 4, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	attachments, err := st.ListRunAttachments(ctx, run.ID)
	if err != nil || len(attachments) != 2 {
		t.Fatalf("bindings=%+v err=%v", attachments, err)
	}
	if _, err := st.GetAttachmentStage(ctx, ready.ID); err != ErrNotFound {
		t.Fatalf("consumed stage err=%v want not found", err)
	}
	retry := &domain.Run{ID: "r2", ProjectID: "p", ServiceID: "s", Prompt: "x", Status: domain.StatusQueued, CreatedAt: now, TriggeredByUserID: &user, CopyAttachmentsFrom: run.ID}
	if err := st.CreateRun(ctx, retry); err != nil {
		t.Fatal(err)
	}
	cloned, err := st.ListRunAttachments(ctx, retry.ID)
	if err != nil || len(cloned) != 2 || cloned[0].RunID != retry.ID {
		t.Fatalf("retry bindings=%+v err=%v", cloned, err)
	}
}

func TestMemExpiredAttachmentStageKeepsKeyUntilDeleted(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	before := time.Now().UTC()
	stage := &domain.AttachmentStage{ID: "expired", ProjectID: "p", CreatedBy: "u", ObjectKey: "run-inputs/p/expired", DisplayName: "x", SizeBytes: 1, CreatedAt: before.Add(-time.Hour), ExpiresAt: before.Add(-time.Minute)}
	if err := st.CreateAttachmentStage(ctx, stage); err != nil {
		t.Fatal(err)
	}
	stages, err := st.ListExpiredAttachmentStages(ctx, before, 10)
	if err != nil || len(stages) != 1 || stages[0].ObjectKey != stage.ObjectKey {
		t.Fatalf("expired=%+v err=%v", stages, err)
	}
	if err := st.DeleteAttachmentStage(ctx, stage.ID, before); err != nil {
		t.Fatal(err)
	}
	if _, err := st.GetAttachmentStage(ctx, stage.ID); err != ErrNotFound {
		t.Fatalf("deleted stage err=%v", err)
	}
}

func TestMemAttachmentUploadClaimIsSingleWinner(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now()
	stage := &domain.AttachmentStage{ID: "claim", ProjectID: "p", CreatedBy: "u", ObjectKey: "run-inputs/p/claim", DisplayName: "x", SizeBytes: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
	if err := st.CreateAttachmentStage(ctx, stage); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, stage.ID, "p", "u", now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, stage.ID, "p", "u", now); err != ErrConflict {
		t.Fatalf("second claim=%v want conflict", err)
	}
	if !st.ReleaseAttachmentStageUpload(ctx, stage.ID) {
		t.Fatal("owner must release active claim")
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, stage.ID, "p", "u", now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachmentStageUploaded(ctx, stage.ID, 1, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, stage.ID, "p", "u", now); err != ErrConflict {
		t.Fatalf("uploaded claim=%v want conflict", err)
	}
}

func TestMemAttachmentOutstandingQuotaCountAndBytes(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	st := NewMemStore()
	for i := 0; i < 20; i++ {
		s := &domain.AttachmentStage{ID: fmt.Sprintf("count-%d", i), ProjectID: "p", CreatedBy: "u", ObjectKey: fmt.Sprintf("k-%d", i), DisplayName: "x", SizeBytes: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := st.CreateAttachmentStage(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateAttachmentStage(ctx, &domain.AttachmentStage{ID: "count-over", ProjectID: "p", CreatedBy: "u", ObjectKey: "k-over", DisplayName: "x", SizeBytes: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != ErrAttachmentQuotaExceeded {
		t.Fatalf("count quota=%v", err)
	}
	st = NewMemStore()
	for i := 0; i < 10; i++ {
		s := &domain.AttachmentStage{ID: fmt.Sprintf("bytes-%d", i), ProjectID: "p", CreatedBy: "u", ObjectKey: fmt.Sprintf("b-%d", i), DisplayName: "x", SizeBytes: 25 << 20, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}
		if err := st.CreateAttachmentStage(ctx, s); err != nil {
			t.Fatal(err)
		}
	}
	if err := st.CreateAttachmentStage(ctx, &domain.AttachmentStage{ID: "bytes-over", ProjectID: "p", CreatedBy: "u", ObjectKey: "b-over", DisplayName: "x", SizeBytes: 1, CreatedAt: now, ExpiresAt: now.Add(time.Hour)}); err != ErrAttachmentQuotaExceeded {
		t.Fatalf("byte quota=%v", err)
	}
}

func TestMemAttachmentObjectGCWaitsForFinalSharedRunReference(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	key := "run-inputs/p/shared"
	st.runs["r1"] = domain.Run{ID: "r1"}
	st.runs["r2"] = domain.Run{ID: "r2"}
	st.runAttachments["r1"] = []domain.RunAttachment{{RunID: "r1", StageID: "stage", ObjectKey: key}}
	st.runAttachments["r2"] = []domain.RunAttachment{{RunID: "r2", StageID: "stage", ObjectKey: key}}
	if keys, _ := st.ListOrphanedRunAttachmentObjects(ctx, 10); len(keys) != 0 {
		t.Fatalf("live shared object eligible early: %v", keys)
	}
	delete(st.runs, "r1")
	if keys, _ := st.ListOrphanedRunAttachmentObjects(ctx, 10); len(keys) != 0 {
		t.Fatalf("one shared ref still live: %v", keys)
	}
	delete(st.runs, "r2")
	keys, _ := st.ListOrphanedRunAttachmentObjects(ctx, 10)
	if len(keys) != 1 || keys[0] != key {
		t.Fatalf("final orphan keys=%v", keys)
	}
	if err := st.DeleteOrphanedRunAttachmentObject(ctx, key); err != nil {
		t.Fatal(err)
	}
	if len(st.runAttachments) != 0 {
		t.Fatalf("orphan metadata not deleted: %+v", st.runAttachments)
	}
}

func TestMemProjectDeleteDoesNotDiscardExpiredStageObjectKey(t *testing.T) {
	ctx := context.Background()
	st := NewMemStore()
	now := time.Now()
	p := &domain.Project{ID: "p", Name: "p", CreatedAt: now}
	if err := st.CreateProject(ctx, p); err != nil {
		t.Fatal(err)
	}
	stage := &domain.AttachmentStage{ID: "s", ProjectID: p.ID, CreatedBy: "deleted-user", ObjectKey: "run-inputs/p/s", DisplayName: "x", SizeBytes: 1, CreatedAt: now.Add(-time.Hour), ExpiresAt: now.Add(-time.Minute)}
	if err := st.CreateAttachmentStage(ctx, stage); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteProject(ctx, p.ID); err != nil {
		t.Fatal(err)
	}
	got, err := st.GetAttachmentStage(ctx, stage.ID)
	if err != nil || got.ObjectKey != stage.ObjectKey {
		t.Fatalf("project delete lost GC metadata: %+v err=%v", got, err)
	}
}

func TestPGAttachmentStagesConsumeAtomicallyAndOnlyOneRunWins(t *testing.T) {
	ctx := context.Background()
	st, seedRun, userID, prefix := pgAttachmentFixture(t)
	now := time.Now().UTC()
	ready := createPGAttachmentStage(t, st, seedRun.ProjectID, userID, domain.NewID(), prefix+"ready", 3, true)
	pending := createPGAttachmentStage(t, st, seedRun.ProjectID, userID, domain.NewID(), prefix+"pending", 4, false)
	run := &domain.Run{
		ID: domain.NewID(), ProjectID: seedRun.ProjectID, ServiceID: seedRun.ServiceID,
		Prompt: "atomic", Status: domain.StatusQueued, Attempt: 1, CreatedAt: now,
		TriggeredByUserID: &userID, AttachmentStageIDs: []string{ready.ID, pending.ID},
	}
	if err := st.CreateRun(ctx, run); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unuploaded stage create run err=%v want ErrNotFound", err)
	}
	if _, err := st.GetRun(ctx, run.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("failed attachment transaction left Run: %v", err)
	}
	if _, err := st.GetAttachmentStage(ctx, ready.ID); err != nil {
		t.Fatalf("failed transaction consumed ready stage: %v", err)
	}
	if attachments, err := st.ListRunAttachments(ctx, run.ID); err != nil || len(attachments) != 0 {
		t.Fatalf("failed transaction left bindings=%+v err=%v", attachments, err)
	}
	if _, err := st.ClaimAttachmentStageUpload(ctx, pending.ID, seedRun.ProjectID, userID, now); err != nil {
		t.Fatal(err)
	}
	if err := st.MarkAttachmentStageUploaded(ctx, pending.ID, pending.SizeBytes, now); err != nil {
		t.Fatal(err)
	}
	if err := st.CreateRun(ctx, run); err != nil {
		t.Fatal(err)
	}
	if attachments, err := st.ListRunAttachments(ctx, run.ID); err != nil || len(attachments) != 2 {
		t.Fatalf("committed attachment bindings=%+v err=%v", attachments, err)
	}

	contended := createPGAttachmentStage(t, st, seedRun.ProjectID, userID, domain.NewID(), prefix+"contended", 5, true)
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			candidate := &domain.Run{
				ID: domain.NewID(), ProjectID: seedRun.ProjectID, ServiceID: seedRun.ServiceID,
				Prompt: fmt.Sprintf("contender-%d", index), Status: domain.StatusQueued, Attempt: 1, CreatedAt: now,
				TriggeredByUserID: &userID, AttachmentStageIDs: []string{contended.ID},
			}
			results <- st.CreateRun(ctx, candidate)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var winners, conflicts int
	for err := range results {
		switch {
		case err == nil:
			winners++
		case errors.Is(err, ErrNotFound):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent stage consume error: %v", err)
		}
	}
	if winners != 1 || conflicts != 1 {
		t.Fatalf("stage consume winners=%d conflicts=%d want 1,1", winners, conflicts)
	}
	var bindings int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM run_attachment_bindings WHERE stage_id=$1`, contended.ID).Scan(&bindings); err != nil || bindings != 1 {
		t.Fatalf("contended stage bindings=%d err=%v want 1,nil", bindings, err)
	}
}

func TestPGAttachmentQuotaAndUploadClaimAreSingleWinner(t *testing.T) {
	ctx := context.Background()
	st, seedRun, userID, prefix := pgAttachmentFixture(t)
	now := time.Now().UTC()
	for i := 0; i < 19; i++ {
		createPGAttachmentStage(t, st, seedRun.ProjectID, userID, domain.NewID(), fmt.Sprintf("%squota-%02d", prefix, i), 1, false)
	}
	start := make(chan struct{})
	results := make(chan error, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			<-start
			stage := &domain.AttachmentStage{
				ID: domain.NewID(), ProjectID: seedRun.ProjectID, CreatedBy: userID,
				ObjectKey: fmt.Sprintf("%squota-race-%d", prefix, index), DisplayName: "x", SizeBytes: 1,
				CreatedAt: now, ExpiresAt: now.Add(time.Hour),
			}
			results <- st.CreateAttachmentStage(ctx, stage)
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var quotaWinner, quotaRejected int
	for err := range results {
		switch {
		case err == nil:
			quotaWinner++
		case errors.Is(err, ErrAttachmentQuotaExceeded):
			quotaRejected++
		default:
			t.Fatalf("unexpected concurrent quota error: %v", err)
		}
	}
	if quotaWinner != 1 || quotaRejected != 1 {
		t.Fatalf("quota winners=%d rejected=%d want 1,1", quotaWinner, quotaRejected)
	}
	var outstanding int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM run_attachment_stages WHERE project_id=$1 AND created_by_user_id=$2 AND expires_at>now()`, seedRun.ProjectID, userID).Scan(&outstanding); err != nil || outstanding != 20 {
		t.Fatalf("outstanding stages=%d err=%v want 20,nil", outstanding, err)
	}

	claimUser := "claim-" + userID
	claim := createPGAttachmentStage(t, st, seedRun.ProjectID, claimUser, domain.NewID(), prefix+"claim", 1, false)
	claimResults := make(chan error, 2)
	start = make(chan struct{})
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			_, err := st.ClaimAttachmentStageUpload(ctx, claim.ID, seedRun.ProjectID, claimUser, now)
			claimResults <- err
		}()
	}
	close(start)
	wg.Wait()
	close(claimResults)
	var claimWinner, claimRejected int
	for err := range claimResults {
		switch {
		case err == nil:
			claimWinner++
		case errors.Is(err, ErrConflict):
			claimRejected++
		default:
			t.Fatalf("unexpected concurrent claim error: %v", err)
		}
	}
	if claimWinner != 1 || claimRejected != 1 {
		t.Fatalf("claim winners=%d rejected=%d want 1,1", claimWinner, claimRejected)
	}
}

func TestPGAttachmentObjectGCWaitsForFinalRetryReference(t *testing.T) {
	ctx := context.Background()
	st, seedRun, userID, prefix := pgAttachmentFixture(t)
	now := time.Now().UTC()
	stage := createPGAttachmentStage(t, st, seedRun.ProjectID, userID, domain.NewID(), prefix+"shared", 9, true)
	first := &domain.Run{
		ID: domain.NewID(), ProjectID: seedRun.ProjectID, ServiceID: seedRun.ServiceID,
		Prompt: "first", Status: domain.StatusQueued, Attempt: 1, CreatedAt: now,
		TriggeredByUserID: &userID, AttachmentStageIDs: []string{stage.ID},
	}
	if err := st.CreateRun(ctx, first); err != nil {
		t.Fatal(err)
	}
	retry := &domain.Run{
		ID: domain.NewID(), ProjectID: seedRun.ProjectID, ServiceID: seedRun.ServiceID,
		Prompt: "retry", Status: domain.StatusQueued, Attempt: 2, CreatedAt: now,
		TriggeredByUserID: &userID, CopyAttachmentsFrom: first.ID,
	}
	if err := st.CreateRun(ctx, retry); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteOrphanedRunAttachmentObject(ctx, stage.ObjectKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("live shared object GC err=%v want ErrNotFound", err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM runs WHERE id=$1`, first.ID); err != nil {
		t.Fatal(err)
	}
	if err := st.DeleteOrphanedRunAttachmentObject(ctx, stage.ObjectKey); !errors.Is(err, ErrNotFound) {
		t.Fatalf("retry still references shared object, GC err=%v want ErrNotFound", err)
	}
	if _, err := st.Pool().Exec(ctx, `DELETE FROM runs WHERE id=$1`, retry.ID); err != nil {
		t.Fatal(err)
	}
	keys, err := st.ListOrphanedRunAttachmentObjects(ctx, 10000)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, key := range keys {
		if key == stage.ObjectKey {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("final shared object was not listed for GC: %v", keys)
	}
	if err := st.DeleteOrphanedRunAttachmentObject(ctx, stage.ObjectKey); err != nil {
		t.Fatal(err)
	}
	var bindings int
	if err := st.Pool().QueryRow(ctx, `SELECT count(*) FROM run_attachment_bindings WHERE object_key=$1`, stage.ObjectKey).Scan(&bindings); err != nil || bindings != 0 {
		t.Fatalf("orphan bindings=%d err=%v want 0,nil", bindings, err)
	}
}
