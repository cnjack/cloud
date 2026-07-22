package store

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strconv"
	"testing"
	"time"

	"github.com/cnjack/jcloud/internal/domain"
)

// deviceRelayStore is the subset of Store the relay tests exercise, so the
// same suite runs against MemStore always and PGStore when JCLOUD_PG_DSN is
// set (mirroring the pgTestStore gating).
type deviceRelayStore interface {
	CreateDevice(context.Context, *domain.Device) error
	GetDevice(context.Context, string) (*domain.Device, error)
	FindDeviceByFingerprint(context.Context, string, string) (*domain.Device, error)
	RevokeDevice(context.Context, string, time.Time) error
	CreateDeviceToken(context.Context, *domain.DeviceToken) error
	GetDeviceTokenByHash(context.Context, string) (*domain.DeviceToken, error)
	UpdateDeviceCapabilities(context.Context, string, []byte) error
	UpsertDeviceSession(context.Context, *domain.DeviceSession) error
	ListDeviceSessions(context.Context, string) ([]domain.DeviceSession, error)
	DeleteDeviceSession(context.Context, string, string) error
	DeleteDeviceSessionsExcept(context.Context, string, []string) error
	AppendDeviceEvents(context.Context, string, string, []*domain.DeviceEvent) (*DeviceEventBatch, error)
	ListDeviceEvents(context.Context, string, string, int64, int) ([]domain.DeviceEvent, error)
	MaxDeviceEventSeq(context.Context, string, string) (int64, error)
	CreateDeviceCommand(context.Context, *domain.DeviceCommand) error
	GetDeviceCommand(context.Context, string, string) (*domain.DeviceCommand, error)
	DeliverPendingDeviceCommands(context.Context, string, int) ([]domain.DeviceCommand, error)
	AckDeviceCommand(context.Context, string, string, string, []byte, time.Time) error
	ListDevicesForUser(context.Context, string) ([]domain.Device, error)
}

func mkEvent(deviceID, sessionID string, seq int64, kind string) *domain.DeviceEvent {
	return &domain.DeviceEvent{
		DeviceID: deviceID, SessionID: sessionID,
		Seq: seq, Kind: kind, Envelope: []byte(`{"n":` + strconv.FormatInt(seq, 10) + `}`),
		CreatedAt: time.Now().UTC(),
	}
}

func testDeviceRelaySessions(t *testing.T, st deviceRelayStore, deviceID string) {
	t.Helper()
	ctx := context.Background()

	// Insert then upsert: meta/status/updated_at are overwritten wholesale.
	first := time.Now().UTC()
	ds := &domain.DeviceSession{DeviceID: deviceID, SessionID: "s1", Status: domain.DeviceSessionRunning, Meta: []byte(`{"title":"a"}`), UpdatedAt: first}
	if err := st.UpsertDeviceSession(ctx, ds); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	second := first.Add(time.Minute)
	ds2 := &domain.DeviceSession{DeviceID: deviceID, SessionID: "s1", Status: domain.DeviceSessionIdle, Meta: []byte(`{"title":"b"}`), UpdatedAt: second}
	if err := st.UpsertDeviceSession(ctx, ds2); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	// A second session, older, to check ordering (updated_at desc).
	older := &domain.DeviceSession{DeviceID: deviceID, SessionID: "s2", Status: domain.DeviceSessionIdle, UpdatedAt: first}
	if err := st.UpsertDeviceSession(ctx, older); err != nil {
		t.Fatalf("upsert s2: %v", err)
	}

	list, err := st.ListDeviceSessions(ctx, deviceID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 || list[0].SessionID != "s1" || list[1].SessionID != "s2" {
		t.Fatalf("list order/content: %+v", list)
	}
	got := list[0]
	if got.Status != domain.DeviceSessionIdle || string(got.Meta) != `{"title":"b"}` {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	// A full device snapshot removes mirrors (and their event logs) that are
	// no longer present locally, while preserving the explicitly retained row.
	if _, err := st.AppendDeviceEvents(ctx, deviceID, "s2", []*domain.DeviceEvent{mkEvent(deviceID, "s2", 1, "user")}); err != nil {
		t.Fatalf("append s2 event: %v", err)
	}
	if err := st.DeleteDeviceSessionsExcept(ctx, deviceID, []string{"s1"}); err != nil {
		t.Fatalf("delete sessions except s1: %v", err)
	}
	list, err = st.ListDeviceSessions(ctx, deviceID)
	if err != nil || len(list) != 1 || list[0].SessionID != "s1" {
		t.Fatalf("list after reconcile = %+v err=%v, want only s1", list, err)
	}
	events, err := st.ListDeviceEvents(ctx, deviceID, "s2", 0, 10)
	if err != nil || len(events) != 0 {
		t.Fatalf("deleted session events = %+v err=%v, want none", events, err)
	}
	if err := st.DeleteDeviceSession(ctx, deviceID, "s1"); err != nil {
		t.Fatalf("delete s1: %v", err)
	}
	list, err = st.ListDeviceSessions(ctx, deviceID)
	if err != nil || len(list) != 0 {
		t.Fatalf("list after delete = %+v err=%v, want empty", list, err)
	}
}

func testDeviceRelayEvents(t *testing.T, st deviceRelayStore, deviceID string) {
	t.Helper()
	ctx := context.Background()
	sid := "s-events"

	// Empty log: max seq 0 (the connector's fresh-start cursor).
	if max, err := st.MaxDeviceEventSeq(ctx, deviceID, sid); err != nil || max != 0 {
		t.Fatalf("empty max seq = %d err=%v, want 0", max, err)
	}

	batch1 := []*domain.DeviceEvent{mkEvent(deviceID, sid, 1, "user"), mkEvent(deviceID, sid, 2, "assistant"), mkEvent(deviceID, sid, 3, "tool_call")}
	res, err := st.AppendDeviceEvents(ctx, deviceID, sid, batch1)
	if err != nil {
		t.Fatalf("append: %v", err)
	}
	if len(res.Accepted) != 3 || len(res.Conflicted) != 0 || res.MaxSeq != 3 {
		t.Fatalf("batch1 = %+v want accepted 3 / conflicted 0 / max 3", res)
	}

	// Replay with an overlap: (device_id, session_id, seq) conflicts skip.
	batch2 := []*domain.DeviceEvent{mkEvent(deviceID, sid, 2, "assistant"), mkEvent(deviceID, sid, 3, "tool_call"), mkEvent(deviceID, sid, 4, "tool_result")}
	res, err = st.AppendDeviceEvents(ctx, deviceID, sid, batch2)
	if err != nil {
		t.Fatalf("replay append: %v", err)
	}
	if len(res.Accepted) != 1 || res.Accepted[0] != 4 || len(res.Conflicted) != 2 || res.MaxSeq != 4 {
		t.Fatalf("batch2 = %+v want accepted [4] / conflicted [2,3] / max 4", res)
	}

	// Replay is ascending from after_seq, limited.
	evs, err := st.ListDeviceEvents(ctx, deviceID, sid, 0, 100)
	if err != nil || len(evs) != 4 {
		t.Fatalf("list = %v err=%v want 4", len(evs), err)
	}
	for i, ev := range evs {
		if ev.Seq != int64(i+1) {
			t.Fatalf("list not ascending/gapless: %+v", evs)
		}
	}
	evs, _ = st.ListDeviceEvents(ctx, deviceID, sid, 2, 1)
	if len(evs) != 1 || evs[0].Seq != 3 {
		t.Fatalf("after_seq+limit = %+v want [seq 3]", evs)
	}
	// Payload round-trips verbatim.
	if string(evs[0].Envelope) != `{"n":3}` {
		t.Fatalf("envelope = %q want verbatim payload", evs[0].Envelope)
	}

	// A different session's log is independent.
	if max, _ := st.MaxDeviceEventSeq(ctx, deviceID, "other"); max != 0 {
		t.Fatalf("cross-session max seq = %d want 0", max)
	}
}

func testDeviceRelayEventsBeforeSessionUpsert(t *testing.T, st deviceRelayStore, deviceID string) {
	t.Helper()
	ctx := context.Background()
	sid := "s-auto"

	// Events land BEFORE any session upsert (the connector batches durable
	// events every ~200ms but only mirrors session rows on its 2s sync tick):
	// the append auto-creates a minimal session row, so nothing fails and the
	// log replays immediately.
	batch := []*domain.DeviceEvent{mkEvent(deviceID, sid, 1, "user"), mkEvent(deviceID, sid, 2, "assistant")}
	res, err := st.AppendDeviceEvents(ctx, deviceID, sid, batch)
	if err != nil {
		t.Fatalf("append before session upsert: %v", err)
	}
	if len(res.Accepted) != 2 || len(res.Conflicted) != 0 || res.MaxSeq != 2 {
		t.Fatalf("batch = %+v want accepted 2 / conflicted 0 / max 2", res)
	}
	evs, err := st.ListDeviceEvents(ctx, deviceID, sid, 0, 100)
	if err != nil || len(evs) != 2 {
		t.Fatalf("replay = %d events err=%v want 2 readable immediately", len(evs), err)
	}

	find := func() *domain.DeviceSession {
		sessions, err := st.ListDeviceSessions(ctx, deviceID)
		if err != nil {
			t.Fatalf("list sessions: %v", err)
		}
		for i := range sessions {
			if sessions[i].SessionID == sid {
				return &sessions[i]
			}
		}
		return nil
	}

	// The placeholder row is a bare running session with empty meta.
	auto := find()
	if auto == nil || auto.Status != domain.DeviceSessionRunning || len(auto.Meta) != 0 {
		t.Fatalf("auto-created session = %+v want running with empty meta", auto)
	}

	// The connector's regular upsert lands later and fills meta/status in
	// without disturbing the events.
	if err := st.UpsertDeviceSession(ctx, &domain.DeviceSession{
		DeviceID: deviceID, SessionID: sid, Status: domain.DeviceSessionIdle,
		Meta: []byte(`{"title":"late"}`), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("late upsert: %v", err)
	}
	auto = find()
	if auto == nil || auto.Status != domain.DeviceSessionIdle || string(auto.Meta) != `{"title":"late"}` {
		t.Fatalf("session after late upsert = %+v want idle with meta", auto)
	}
	if evs, _ = st.ListDeviceEvents(ctx, deviceID, sid, 0, 100); len(evs) != 2 {
		t.Fatalf("events after late upsert = %d want 2", len(evs))
	}
}

func testDeviceRelayCommands(t *testing.T, st deviceRelayStore, d *domain.Device) {
	t.Helper()
	ctx := context.Background()
	deviceID := d.ID

	// A second device, so "another device's command" is a real row (PG enforces
	// the device_commands → devices FK).
	otherDevice := &domain.Device{
		ID: domain.NewID(), UserID: d.UserID, Name: "other-box", KeyGen: 1,
		CreatedAt: time.Now().UTC(),
	}
	if err := st.CreateDevice(ctx, otherDevice); err != nil {
		t.Fatalf("create other device: %v", err)
	}

	mk := func(id, kind string, sid *string, at time.Time) *domain.DeviceCommand {
		return &domain.DeviceCommand{
			ID: id, DeviceID: deviceID, Kind: kind, SessionID: sid,
			Envelope: []byte(`{}`), Status: domain.DeviceCommandPending, CreatedAt: at,
		}
	}
	now := time.Now().UTC()
	sid := "s1"
	if err := st.CreateDeviceCommand(ctx, mk("c1", domain.DeviceCmdChatSend, nil, now)); err != nil {
		t.Fatalf("create c1: %v", err)
	}
	if err := st.CreateDeviceCommand(ctx, mk("c2", domain.DeviceCmdChatStop, &sid, now.Add(time.Second))); err != nil {
		t.Fatalf("create c2: %v", err)
	}
	// Another device's command is invisible.
	other := mk("c9", domain.DeviceCmdChatStop, &sid, now)
	other.DeviceID = otherDevice.ID
	if err := st.CreateDeviceCommand(ctx, other); err != nil {
		t.Fatalf("create other: %v", err)
	}

	// Offer: both pending, oldest first, flipped to delivered atomically.
	cmds, err := st.DeliverPendingDeviceCommands(ctx, deviceID, 64)
	if err != nil {
		t.Fatalf("deliver: %v", err)
	}
	if len(cmds) != 2 || cmds[0].ID != "c1" || cmds[1].ID != "c2" {
		t.Fatalf("delivered = %+v want [c1 c2]", cmds)
	}
	for _, c := range cmds {
		if c.Status != domain.DeviceCommandDelivered {
			t.Fatalf("command %s status = %q want delivered", c.ID, c.Status)
		}
	}
	// Nothing left to offer (the other device's command is untouched).
	if cmds, _ = st.DeliverPendingDeviceCommands(ctx, deviceID, 64); len(cmds) != 0 {
		t.Fatalf("re-deliver = %+v want empty", cmds)
	}

	// Ack ok stores the result and stamps acked_at.
	if err := st.AckDeviceCommand(ctx, deviceID, "c1", domain.DeviceCommandAcked, []byte(`{"ok":true}`), now.Add(2*time.Second)); err != nil {
		t.Fatalf("ack c1: %v", err)
	}
	got, err := st.GetDeviceCommand(ctx, deviceID, "c1")
	if err != nil || got.Status != domain.DeviceCommandAcked || string(got.Result) != `{"ok":true}` || got.AckedAt == nil {
		t.Fatalf("get acked command = %+v err=%v", got, err)
	}
	if _, err := st.GetDeviceCommand(ctx, otherDevice.ID, "c1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("get command through other device: err=%v want ErrNotFound", err)
	}
	// A duplicate ack is an idempotent no-op.
	if err := st.AckDeviceCommand(ctx, deviceID, "c1", domain.DeviceCommandFailed, []byte(`{"ok":false}`), now.Add(3*time.Second)); err != nil {
		t.Fatalf("duplicate ack: %v", err)
	}
	// Ack error marks failed.
	if err := st.AckDeviceCommand(ctx, deviceID, "c2", domain.DeviceCommandFailed, []byte(`{"err":"boom"}`), now.Add(2*time.Second)); err != nil {
		t.Fatalf("ack c2: %v", err)
	}
	// Unknown command, and ANOTHER device's command: both ErrNotFound
	// (indistinguishable on purpose).
	if err := st.AckDeviceCommand(ctx, deviceID, "nope", domain.DeviceCommandAcked, nil, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ack unknown: err=%v want ErrNotFound", err)
	}
	if err := st.AckDeviceCommand(ctx, deviceID, "c9", domain.DeviceCommandAcked, nil, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("ack other's: err=%v want ErrNotFound", err)
	}
}

func testDeviceRelayListForUser(t *testing.T, st deviceRelayStore, deviceID, userID string) {
	t.Helper()
	ctx := context.Background()
	devices, err := st.ListDevicesForUser(ctx, userID)
	if err != nil {
		t.Fatalf("list for user: %v", err)
	}
	found := false
	for _, dev := range devices {
		if dev.UserID != userID {
			t.Fatalf("list leaked another user's device: %+v", dev)
		}
		if dev.ID == deviceID {
			found = true
		}
	}
	if !found {
		t.Fatalf("list for user = %+v, device %s missing", devices, deviceID)
	}
	if devices, _ = st.ListDevicesForUser(ctx, "someone-else"); len(devices) != 0 {
		t.Fatalf("other user's list = %+v want empty", devices)
	}
}

// testDeviceCapabilities covers the M12 compose-capability mirror: the blob
// round-trips verbatim (JSONB on PG, plain bytes on memory) and a nil update
// clears it.
func testDeviceCapabilities(t *testing.T, st deviceRelayStore, deviceID string) {
	t.Helper()
	ctx := context.Background()

	d, err := st.GetDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("get device: %v", err)
	}
	if d.Capabilities != nil {
		t.Fatalf("fresh device capabilities = %s want nil", d.Capabilities)
	}

	caps := []byte(`{"projects":[{"path":"/repo","name":"repo"}],"models":[{"provider":"anthropic","id":"claude-opus-4-1","label":"Opus 4.1"}],"efforts":["low","high"]}`)
	if err := st.UpdateDeviceCapabilities(ctx, deviceID, caps); err != nil {
		t.Fatalf("update capabilities: %v", err)
	}
	d, err = st.GetDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("re-get device: %v", err)
	}
	// Semantic compare, not byte-verbatim: PG stores JSONB (which normalizes
	// key order/whitespace) while MemStore keeps the raw bytes — both must
	// decode to the same value.
	var gotCaps, wantCaps any
	if err := json.Unmarshal(d.Capabilities, &gotCaps); err != nil {
		t.Fatalf("stored capabilities not JSON: %v (%s)", err, d.Capabilities)
	}
	if err := json.Unmarshal(caps, &wantCaps); err != nil {
		t.Fatalf("test fixture not JSON: %v", err)
	}
	if !reflect.DeepEqual(gotCaps, wantCaps) {
		t.Fatalf("capabilities = %s want %s", d.Capabilities, caps)
	}

	if err := st.UpdateDeviceCapabilities(ctx, deviceID, nil); err != nil {
		t.Fatalf("clear capabilities: %v", err)
	}
	d, err = st.GetDevice(ctx, deviceID)
	if err != nil {
		t.Fatalf("re-get cleared device: %v", err)
	}
	if d.Capabilities != nil {
		t.Fatalf("cleared capabilities = %s want nil", d.Capabilities)
	}

	if err := st.UpdateDeviceCapabilities(ctx, "no-such-device", caps); !errors.Is(err, ErrNotFound) {
		t.Fatalf("unknown device: err=%v want ErrNotFound", err)
	}
}

// --- MemStore -----------------------------------------------------------------

func TestDeviceRelayMemStore(t *testing.T) {
	m := NewMemStore()
	d := mkDevice(t, m, "user-1")
	t.Run("capabilities", func(t *testing.T) { testDeviceCapabilities(t, m, d.ID) })
	t.Run("sessions", func(t *testing.T) { testDeviceRelaySessions(t, m, d.ID) })
	t.Run("events", func(t *testing.T) { testDeviceRelayEvents(t, m, d.ID) })
	t.Run("eventsBeforeUpsert", func(t *testing.T) { testDeviceRelayEventsBeforeSessionUpsert(t, m, d.ID) })
	t.Run("commands", func(t *testing.T) { testDeviceRelayCommands(t, m, d) })
	t.Run("listForUser", func(t *testing.T) { testDeviceRelayListForUser(t, m, d.ID, "user-1") })
	t.Run("fingerprintAndRevoke", func(t *testing.T) { testDeviceFingerprintAndRevoke(t, m, "user-1", "user-2") })
}

// testDeviceFingerprintAndRevoke covers the M16 store contract: the
// (user_id, fingerprint_hash) dedup invariant, the fingerprint lookup, and
// RevokeDevice's soft-delete semantics (idempotency, list filtering, token
// kill via the devices join, fingerprint freed for re-login).
func testDeviceFingerprintAndRevoke(t *testing.T, st deviceRelayStore, userID, otherUserID string) {
	t.Helper()
	ctx := context.Background()
	const fpA = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const fpB = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	mk := func(hash string) *domain.Device {
		return &domain.Device{
			ID: domain.NewID(), UserID: userID, Name: "fp-box", KeyGen: 1,
			FingerprintHash: hash, CreatedAt: time.Now().UTC(),
		}
	}

	// Empty-string fingerprints never collide (pre-M16 devices coexist).
	if err := st.CreateDevice(ctx, mk("")); err != nil {
		t.Fatalf("create fingerprint-free device: %v", err)
	}
	if err := st.CreateDevice(ctx, mk("")); err != nil {
		t.Fatalf("second fingerprint-free device must not collide: %v", err)
	}

	a := mk(fpA)
	if err := st.CreateDevice(ctx, a); err != nil {
		t.Fatalf("create device A: %v", err)
	}
	// A second NON-REVOKED device with the same (user, hash) is rejected.
	if err := st.CreateDevice(ctx, mk(fpA)); !errors.Is(err, ErrAlreadyExists) {
		t.Fatalf("duplicate fingerprint: err=%v want ErrAlreadyExists", err)
	}
	// ... but another user may hold the same hash, and another hash is fine.
	otherUser := mk(fpA)
	otherUser.UserID = otherUserID
	if err := st.CreateDevice(ctx, otherUser); err != nil {
		t.Fatalf("same fingerprint for another user: %v", err)
	}
	if err := st.CreateDevice(ctx, mk(fpB)); err != nil {
		t.Fatalf("different fingerprint: %v", err)
	}

	// The lookup finds only the user's own live row.
	got, err := st.FindDeviceByFingerprint(ctx, userID, fpA)
	if err != nil || got.ID != a.ID {
		t.Fatalf("find by fingerprint: %+v err=%v, want device A", got, err)
	}
	if _, err := st.FindDeviceByFingerprint(ctx, userID, "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("find unknown fingerprint: err=%v want ErrNotFound", err)
	}
	if _, err := st.FindDeviceByFingerprint(ctx, otherUserID+"-none", fpA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("find another user's fingerprint: err=%v want ErrNotFound", err)
	}

	// RevokeDevice: stamps revoked_at; a repeat is ErrNotFound; the row drops
	// out of ListDevicesForUser; its tokens stop resolving.
	now := time.Now().UTC()
	tok := &domain.DeviceToken{ID: domain.NewID(), DeviceID: a.ID, TokenHash: "hash-a", CreatedAt: now}
	if err := st.CreateDeviceToken(ctx, tok); err != nil {
		t.Fatalf("create token: %v", err)
	}
	if err := st.RevokeDevice(ctx, a.ID, now); err != nil {
		t.Fatalf("revoke device: %v", err)
	}
	if err := st.RevokeDevice(ctx, a.ID, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("re-revoke: err=%v want ErrNotFound", err)
	}
	if err := st.RevokeDevice(ctx, "no-such-device", now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoke unknown: err=%v want ErrNotFound", err)
	}
	if _, err := st.GetDeviceTokenByHash(ctx, "hash-a"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device's token still resolves: err=%v want ErrNotFound", err)
	}
	devices, err := st.ListDevicesForUser(ctx, userID)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	for _, d := range devices {
		if d.ID == a.ID {
			t.Fatalf("revoked device still listed: %+v", d)
		}
	}
	if _, err := st.FindDeviceByFingerprint(ctx, userID, fpA); !errors.Is(err, ErrNotFound) {
		t.Fatalf("revoked device still claims its fingerprint: err=%v want ErrNotFound", err)
	}
	// The freed fingerprint can be claimed by a fresh row (re-login).
	fresh := mk(fpA)
	if err := st.CreateDevice(ctx, fresh); err != nil {
		t.Fatalf("re-login after revoke: %v", err)
	}
	got, err = st.FindDeviceByFingerprint(ctx, userID, fpA)
	if err != nil || got.ID != fresh.ID {
		t.Fatalf("find after re-login: %+v err=%v, want the fresh row", got, err)
	}
}
