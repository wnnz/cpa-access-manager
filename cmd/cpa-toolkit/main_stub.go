//go:build !cgo

package main

func main() {
	panic("cpa-toolkit native plugin build requires CGO_ENABLED=1")
}
