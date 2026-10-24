module github.com/MioYiSama/moonuestc

go 1.27.3

require (
	golang.org/x/sys v0.34.0
	modernc.org/sqlite v1.38.2
	yyb_go/moonuestcauth v0.0.0
)

require (
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/mattn/go-isatty v0.0.20 // indirect
	github.com/ncruces/go-strftime v0.1.9 // indirect
	github.com/remyoudompheng/bigfft v0.0.0-20230129092748-24d4a6f8daec // indirect
	golang.org/x/exp v0.0.0-20250620022241-b7579e27df2b // indirect
	modernc.org/libc v1.66.3 // indirect
	modernc.org/mathutil v1.7.1 // indirect
	modernc.org/memory v1.11.0 // indirect
	yyb_go v0.0.0 // indirect
)

replace yyb_go => github.com/525815266/YYB-Go-Enhanced v0.2.34-0.20261023061504-8a9497397abc

replace yyb_go/moonuestcauth => ./internal/yybauth
