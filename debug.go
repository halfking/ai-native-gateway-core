package main

import (
    "encoding/base64"
    "fmt"
)

func main() {
    key := "AwoRGB8mLTQ7QklQV15lbHN6gYiPlp2kq7L5wMfO1dw"
    raw, _ := base64.URLEncoding.DecodeString(key)
    fmt.Printf("Full key (%d bytes): %x\n", len(raw), raw)
    fmt.Printf("Signing key (0-15): %x\n", raw[:16])
    fmt.Printf("Encryption key (16-31): %x\n", raw[16:])
    
    // Test: create a fresh token with EncryptFernet, then decrypt it
    // This will tell us if the implementation is self-consistent
}
