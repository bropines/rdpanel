package reconcile

import "github.com/bropines/rustdesk-panel/internal/hbbs/control"

// KeyInSync reports whether key (one of the Key* constants of target) is
// managed in desired and, if so, whether observed matches it with the same
// equality the reconciler uses for drift detection (hbbr rate rounding, relay
// lists filtered by hbbs, IP lists as sets). An unmanaged key is in sync; a
// managed key with no observed value (server unreachable) is not.
func KeyInSync(target control.Target, key string, desired, observed Settings) (managed, inSync bool) {
	for _, f := range fields() {
		if f.target != target || f.key != key {
			continue
		}
		if f.get(&desired) == nil {
			return false, true
		}
		if f.get(&observed) == nil {
			return true, false
		}
		switch key {
		case KeyRelayServers:
			return true, !relayDrift(observed.RelayServers, desired.RelayServers)
		case KeyAlwaysUseRelay:
			return true, *observed.AlwaysUseRelay == *desired.AlwaysUseRelay
		case KeyMustLogin:
			return true, *observed.MustLogin == *desired.MustLogin
		case KeyTCPKeepalive:
			return true, *observed.TCPKeepalive == *desired.TCPKeepalive
		case KeyBlacklist:
			return true, sameSet(observed.Blacklist, desired.Blacklist)
		case KeyBlocklist:
			return true, sameSet(observed.Blocklist, desired.Blocklist)
		case KeyLimitSpeed:
			return true, rateEqual(*desired.LimitSpeedMbps, *observed.LimitSpeedMbps)
		case KeyTotalBandwidth:
			return true, rateEqual(*desired.TotalBandwidthMbps, *observed.TotalBandwidthMbps)
		case KeySingleBandwidth:
			return true, rateEqual(*desired.SingleBandwidthMbps, *observed.SingleBandwidthMbps)
		case KeyDowngradeThreshold:
			return true, thresholdEqual(*desired.DowngradeThreshold, *observed.DowngradeThreshold)
		case KeyDowngradeStartCheck:
			return true, *desired.DowngradeStartCheckSecs == *observed.DowngradeStartCheckSecs
		}
	}
	return false, true
}

// sameSet compares IP lists ignoring order and duplicates (like diff).
func sameSet(a, b []string) bool {
	add, remove := diff(a, b)
	return len(add) == 0 && len(remove) == 0
}
