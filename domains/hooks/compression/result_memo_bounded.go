package compression

import "errors"

// CompressedBody is base64 in the JSON memo. Bound the encoded envelope before
// Redis GET, and then independently bound decoded body and metadata fields.
const strictMemoWireBytes = ((strictCacheBodyBytes+2)/3)*4 + strictCacheMetaBytes + 4096

const strictMemoReadScript = `-- compression-memo-bounded-read
if redis.call('STRLEN',KEYS[1]) > tonumber(ARGV[1]) then return redis.error_reply('compression memo exceeds bounded read capacity') end
return redis.call('GET',KEYS[1])`

func checkBoundedMemo(val *MemoValue) error {
	if len(val.CompressedBody) > strictCacheBodyBytes {
		return errors.New("memo body exceeds bounded recovery capacity")
	}
	metadata := len(val.Strategy) + len(val.SummaryMarker) + len(val.WindowTriggered) + len(val.CompressedPrefixHash) + len(val.MsgHashes) + len(val.AlignmentMap)
	if metadata > strictCacheMetaBytes {
		return errors.New("memo metadata exceeds bounded recovery capacity")
	}
	return nil
}
