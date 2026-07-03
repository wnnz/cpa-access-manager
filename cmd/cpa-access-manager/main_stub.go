//go:build !cgo

package main

func main() {
	panic("cpa-access-manager native plugin build requires CGO_ENABLED=1")
}
