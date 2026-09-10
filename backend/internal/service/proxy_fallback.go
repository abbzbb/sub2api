package service

import "time"

// ResolveProxyFallbackTarget 计算一个过期代理 start 应把账号改投到哪里。
// 返回 (targetID, change)：
//   - change=false：不改动账号（mode=none，或链路成环/无解的兜底）
//   - change=true, targetID=nil：改投为直连
//   - change=true, targetID!=nil：改投到该备用代理 id
//
// 链路规则：
//   - 仅 StatusActive 且未过期（IsExpired=false）的节点可作为 rebound 目标；
//     inactive / error / expired / 缺失节点不会被选中。
//   - 不可用节点若 FallbackModeProxy，则继续沿 BackupProxyID 往后走。
//   - 不可用节点若 FallbackModeDirect，则改投直连（targetID=nil, change=true）。
//   - 链路耗尽、节点缺失或成环：unresolved（change=false），sweep 将账号留在过期主代理上。
//
// byID 是「全部代理」的快照（id -> Proxy），now 为判定基准时间。
func ResolveProxyFallbackTarget(start Proxy, byID map[int64]Proxy, now time.Time) (*int64, bool) {
	switch start.FallbackMode {
	case FallbackModeDirect:
		return nil, true
	case FallbackModeProxy:
		visited := map[int64]struct{}{start.ID: {}}
		curID := start.BackupProxyID
		for {
			if curID == nil {
				return nil, false
			}
			if _, seen := visited[*curID]; seen {
				return nil, false
			}
			p, ok := byID[*curID]
			if !ok {
				return nil, false
			}
			if isAcceptableProxyFallbackTarget(p, now) {
				id := p.ID
				return &id, true
			}
			visited[*curID] = struct{}{}
			switch p.FallbackMode {
			case FallbackModeDirect:
				return nil, true
			case FallbackModeProxy:
				curID = p.BackupProxyID
			default:
				return nil, false
			}
		}
	default:
		return nil, false
	}
}

// isAcceptableProxyFallbackTarget reports whether p may be a rebound target.
// Only StatusActive + not-expired nodes qualify; inactive/error/expired are skipped.
func isAcceptableProxyFallbackTarget(p Proxy, now time.Time) bool {
	switch p.Status {
	case StatusError, StatusInactive, StatusExpired:
		return false
	case StatusActive:
		return !p.IsExpired(now)
	default:
		return false
	}
}
