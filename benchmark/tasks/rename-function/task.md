Rename the Go function `CalcTotal` in package `invoice` (file `invoice/total.go`) to `ComputeInvoiceTotal`.

Update every reference to it across the module: call sites, tests (including test function names that contain `CalcTotal`) and doc comments that mention it. Do not rename anything else; in particular `shipping.CalcTotalWeight` is a different function and keeps its name.

`go build ./...` and `go test ./...` must pass afterwards.
