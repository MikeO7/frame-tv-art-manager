#!/bin/bash
cd internal/optimize
go test -bench=BenchmarkGenerateSaliencyMap_Prof -benchmem
