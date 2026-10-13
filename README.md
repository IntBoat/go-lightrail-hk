# go-lightrail-hk

香港輕鐵的 Go 客戶端。站序來自港鐵公開 CSV，到站來自 [Light Rail Next Train](https://rt.data.gov.hk/v1/transport/mtr/lrt/getSchedule)。不需要 API 金鑰。

這個庫沒有每一班的鐘點時間表。官方即時 API 回傳的是「還有幾分鐘」「即將抵達」或「正在離開」。同一站、同一路線可以同時有多於一班。

## 引用

```
go get github.com/IntBoat/go-lightrail-hk
```

```go
import "github.com/IntBoat/go-lightrail-hk"

client, err := lightrail.New(nil)
```

`New` 載入內嵌站序，不會上網。`http.Client` 傳 `nil` 時逾時 8 秒。

## 到站

```go
rows, err := client.Next(ctx, 468, "761P", lightrail.Call{})
```

- `stopID` 是官方 Stop ID。頌富是 `468`，元朗是 `600`。
- `route` 為空則回該站全部路線。
- API 沒有方向參數。往元朗要再看 `DestZH == "元朗"` 或 `DestEN == "Yuen Long"`。
- 回傳保留月台順序。同一路線的多班都在。
- `StatusMinutes` 時 `Minutes` 才有意義。`time_en` 為 `-` 時是 `StatusUnknown`，這一班仍保留。
- 請求一定帶 `with_special=1`。`special == 0` 用 `route_no`；`special == 1` 用 `additionalInfo1`，並去掉結尾的 `*`。

## 站序

```go
client.Routes()
client.Stops("761P", 2)
```

`Direction` 是 CSV 的 1 或 2，不是目的地車站編號。同一路線同一方向若終點重複出現，只留序號較小的一筆。

```go
err = client.RefreshCatalogue(ctx, lightrail.Call{})
```

成功才換掉記憶體中的站序。內嵌 CSV 是斷網時的後備。

## 快取

- 同一站的到站快取 10 秒。
- 站序下載成功後 24 小時內不再下載。
- `lightrail.Call{Fresh: true}` 略過對應的那一層快取並重打上游。

`Client` 可以並行使用。上游 HTTP 不是 200、JSON 無效，或 `status` 不是 1，會回錯誤。錯誤不含回應本文。
