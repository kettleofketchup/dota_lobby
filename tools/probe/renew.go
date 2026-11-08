package main

import (
	"context"
	"fmt"
	"sync"

	"github.com/paralin/go-steam"
	"github.com/paralin/go-steam/protocol"
	"github.com/paralin/go-steam/protocol/protobuf/unified"
	"github.com/paralin/go-steam/protocol/steamlang"
)

// emsgServiceMethodCallFromClient is SteamKit's EMsg 151; go-steam's
// steamlang table does not define it.
const emsgServiceMethodCallFromClient = steamlang.EMsg(151)

// renewer asks the logged-on CM for a fresh refresh token, mirroring
// SteamKit's GenerateAccessTokenForAppAsync (gap-01 §3.1). Q12.
type renewer struct {
	sc      *steam.Client
	mu      sync.Mutex
	pending map[protocol.JobId]chan *protocol.Packet
}

func newRenewer(sc *steam.Client) *renewer {
	r := &renewer{sc: sc, pending: map[protocol.JobId]chan *protocol.Packet{}}
	sc.RegisterPacketHandler(r)
	return r
}

func (r *renewer) HandlePacket(p *protocol.Packet) {
	if p.EMsg != steamlang.EMsg_ServiceMethodResponse {
		return
	}
	r.mu.Lock()
	ch := r.pending[p.TargetJobId]
	r.mu.Unlock()
	if ch != nil {
		select {
		case ch <- p:
		default:
		}
	}
}

// renew returns the new refresh token, or the old one if Steam did not issue
// a new one (it only does so near expiry), plus the EResult it reported.
func (r *renewer) renew(ctx context.Context, refresh string, steamID uint64) (string, steamlang.EResult, error) {
	rt := unified.ETokenRenewalType_k_ETokenRenewalType_Allow
	msg := protocol.NewClientMsgProtobuf(emsgServiceMethodCallFromClient, &unified.CAuthentication_AccessToken_GenerateForApp_Request{
		RefreshToken: &refresh, Steamid: &steamID, RenewalType: &rt,
	})
	job := r.sc.GetNextJobId()
	msg.SetSourceJobId(job)
	msg.Header.Proto.TargetJobName = new("Authentication.GenerateAccessTokenForApp#1")

	ch := make(chan *protocol.Packet, 1)
	r.mu.Lock()
	r.pending[job] = ch
	r.mu.Unlock()
	defer func() {
		r.mu.Lock()
		delete(r.pending, job)
		r.mu.Unlock()
	}()

	r.sc.Write(msg)
	select {
	case <-ctx.Done():
		return "", 0, fmt.Errorf("renew: %w", ctx.Err())
	case p := <-ch:
		resp := &unified.CAuthentication_AccessToken_GenerateForApp_Response{}
		m := p.ReadProtoMsg(resp)
		res := steamlang.EResult(m.Header.Proto.GetEresult())
		if res != steamlang.EResult_OK {
			return "", res, fmt.Errorf("renew: %v", res)
		}
		if t := resp.GetRefreshToken(); t != "" {
			return t, res, nil
		}
		return refresh, res, nil
	}
}
