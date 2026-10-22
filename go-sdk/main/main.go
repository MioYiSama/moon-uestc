package main

import (
	"context"
	"fmt"
	"os"
	"time"

	moonuestc "moon-uestc/go-sdk"
)

func main() {
	client, err := moonuestc.NewClient(moonuestc.Options{
		Session: os.Getenv("MOON_SESSION"),
	})
	if err != nil {
		panic(err)
	}
	defer client.CloseIdleConnections()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	user, err := client.Auth.UserInfo(ctx, "")
	if err != nil {
		panic(err)
	}
	fmt.Println(user.Data.UserData.FullName, user.Data.UserData.Role)

	record, err := client.Student.Today(ctx)
	if err != nil {
		panic(err)
	}
	if record.Data != nil {
		fmt.Println(record.Data.BusinessDate, record.Data.RecordStatusCode)
		for _, contact := range record.Data.InstructorList {
			fmt.Println(contact.Name) // JSON-in-a-string 已解码为联系人切片
		}
	}

	history, err := client.Student.History(ctx, moonuestc.HistoryQuery{
		Year: 2026, Month: 10, PageIndex: 1, PageSize: 10,
	})
	if err != nil {
		panic(err)
	}
	for _, item := range history.Data {
		fmt.Println(item.BusinessDate, item.RecordStatusCode)
	}
	if history.Page != nil {
		fmt.Println(history.Page.TotalCount, history.Page.TotalPage)
	}
}
