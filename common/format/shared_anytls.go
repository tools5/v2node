package format

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"
)

const childMarker = "|child:"

func SharedChildPassword(uuid string, id int) string {
	h := hmac.New(sha256.New, []byte(uuid))
	h.Write([]byte("v2nb:anytls-child:v1:" + strconv.Itoa(id)))
	return hex.EncodeToString(h.Sum(nil))
}

func SharedChildTag(tag string, id int) string {
	return tag + childMarker + strconv.Itoa(id)
}

func SharedChildUserTag(tag, uuid string, id int) string {
	return SharedChildTag(UserTag(tag, uuid), id)
}

func SplitSharedChildUser(email string) (canonical string, id int) {
	pos := strings.LastIndex(email, childMarker)
	if pos <= 0 {
		return email, 0
	}
	suffix := email[pos+len(childMarker):]
	n, err := strconv.Atoi(suffix)
	if err != nil || n <= 0 || strconv.Itoa(n) != suffix {
		return email, 0
	}
	return email[:pos], n
}

func CanonicalUserTag(email string) string {
	canonical, _ := SplitSharedChildUser(email)
	return canonical
}
