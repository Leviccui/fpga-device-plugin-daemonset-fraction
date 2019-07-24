package main

const (
	resourceName  = "xilinx.com/fpga-xilinx_u200_xdma_201830_1-1542252769-fraction"
	resourceCount = "xilinx.com/fpga-count"

	OptimisticLockErrorMsg = "the object has been modified; please apply your changes to the latest version and try again"

	EnvResourceIndex      = "PFGA_BIND_IDX"
	EnvAssignedFlag       = "PFGA_IS_ASSIGNED"
	EnvResourceAssumeTime = "FPGA_BIND_TIMESTAMP"

	BlockPerFPGA = 3
)
