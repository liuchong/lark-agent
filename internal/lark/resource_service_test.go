package lark

import (
	"context"
	"net/http"
	"reflect"
	"testing"
)

type resourceRoutingCaller struct {
	requests []APIRequest
	call     func(APIRequest) any
}

func (f *resourceRoutingCaller) CallAPI(_ context.Context, req APIRequest) (any, error) {
	f.requests = append(f.requests, req)
	return f.call(req), nil
}

func TestResourceServiceResolvesWikiBaseAndRecordShare(t *testing.T) {
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		switch req.Path {
		case "/open-apis/wiki/v2/spaces/get_node":
			return map[string]any{"data": map[string]any{"node": map[string]any{
				"obj_type": "bitable", "obj_token": "bas_bug", "title": "Bug 管理",
			}}}
		case "/open-apis/base/v3/record_share/shr_bug/meta":
			return map[string]any{"data": map[string]any{
				"base_token": "bas_bug", "table_id": "tbl_bug", "record_id": "rec_bug",
			}}
		default:
			t.Fatalf("unexpected request=%+v", req)
			return nil
		}
	}}
	service := NewResourceService(caller)
	wiki, err := service.ResolveResource(context.Background(), ResourceRef{
		ResourceType: ResourceTypeWiki, WikiNodeToken: "wik_bug", TableID: "tbl_bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if wiki.ResourceType != ResourceTypeBase || wiki.AppToken != "bas_bug" ||
		wiki.TableID != "tbl_bug" || wiki.FileToken != "bas_bug" || wiki.FileType != "bitable" {
		t.Fatalf("wiki=%+v", wiki)
	}
	record, err := service.ResolveResource(context.Background(), ResourceRef{
		ResourceType: ResourceTypeBaseRecord, RecordShareToken: "shr_bug",
	})
	if err != nil {
		t.Fatal(err)
	}
	if record.AppToken != "bas_bug" || record.TableID != "tbl_bug" || record.RecordID != "rec_bug" {
		t.Fatalf("record=%+v", record)
	}
	if caller.requests[0].As != IdentityUser || caller.requests[1].As != IdentityUser {
		t.Fatalf("requests=%+v", caller.requests)
	}
}

func TestResourceServiceSubscribesAndReadsTypedBaseSchema(t *testing.T) {
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		switch req.Path {
		case "/open-apis/drive/v1/files/bas_bug/subscriptions":
			if req.Method != http.MethodPost || req.As != IdentityUser {
				t.Fatalf("subscription request=%+v", req)
			}
			return map[string]any{"data": map[string]any{
				"subscription_id": "sub_bug", "is_subcribe": true, "file_type": "bitable",
			}}
		case "/open-apis/bitable/v1/apps/bas_bug/tables/tbl_bug/fields":
			return map[string]any{"data": map[string]any{"items": []any{
				map[string]any{"field_id": "fld_title", "field_name": "问题", "type": float64(1), "is_primary": true},
				map[string]any{"field_id": "fld_status", "field_name": "状态", "type": float64(3), "property": map[string]any{
					"options": []any{
						map[string]any{"id": "opt_todo", "name": "待修改"},
						map[string]any{"id": "opt_resolved", "name": "已解决"},
					},
				}},
				map[string]any{"field_id": "fld_owner", "field_name": "经办人", "type": float64(11)},
			}}}
		default:
			t.Fatalf("unexpected request=%+v", req)
			return nil
		}
	}}
	service := NewResourceService(caller)
	remote, err := service.EnsureCommentSubscription(context.Background(), ResourceRef{
		ResourceType: ResourceTypeBase, AppToken: "bas_bug", FileToken: "bas_bug", FileType: "bitable",
	})
	if err != nil {
		t.Fatal(err)
	}
	if remote.ID != "sub_bug" || !remote.Active {
		t.Fatalf("remote=%+v", remote)
	}
	fields, err := service.ListBaseFields(context.Background(), "bas_bug", "tbl_bug")
	if err != nil {
		t.Fatal(err)
	}
	wantOptions := []BaseOption{{ID: "opt_todo", Name: "待修改"}, {ID: "opt_resolved", Name: "已解决"}}
	if len(fields) != 3 || fields[0].Name != "问题" || !fields[0].Primary ||
		!reflect.DeepEqual(fields[1].Options, wantOptions) {
		t.Fatalf("fields=%+v", fields)
	}
}

func TestResourceServiceComparesBeforeUpdatingBaseStatus(t *testing.T) {
	gets := 0
	updates := 0
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		switch req.Method {
		case http.MethodGet:
			gets++
			value := "待修改"
			if gets > 1 {
				value = "已解决"
			}
			return map[string]any{"data": map[string]any{"record": map[string]any{
				"record_id": "rec_bug", "fields": map[string]any{"状态": value},
			}}}
		case http.MethodPut:
			updates++
			return map[string]any{"data": map[string]any{"record": map[string]any{"record_id": "rec_bug"}}}
		default:
			t.Fatalf("unexpected request=%+v", req)
			return nil
		}
	}}
	result, err := NewResourceService(caller).CompareAndUpdateBaseField(
		context.Background(),
		BaseFieldUpdate{
			AppToken: "bas_bug", TableID: "tbl_bug", RecordID: "rec_bug",
			FieldName: "状态", Before: "待修改", After: "已解决",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Before != "待修改" || result.After != "已解决" || !result.Verified ||
		gets != 2 || updates != 1 {
		t.Fatalf("result=%+v gets=%d updates=%d", result, gets, updates)
	}
}

func TestResourceServiceListsBaseRecordsWithOpenIDProjection(t *testing.T) {
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		if req.Path != "/open-apis/bitable/v1/apps/bas_bug/tables/tbl_bug/records" ||
			req.Params["user_id_type"] != "open_id" || req.Params["view_id"] != "vew_bug" {
			t.Fatalf("request=%+v", req)
		}
		return map[string]any{"data": map[string]any{"items": []any{
			map[string]any{"record_id": "rec_bug", "fields": map[string]any{
				"问题": "归档示例条目后列表未刷新",
				"状态": "待修改",
			}},
		}}}
	}}
	records, err := NewResourceService(caller).ListBaseRecords(
		context.Background(), "bas_bug", "tbl_bug", "vew_bug",
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != "rec_bug" || records[0].Fields["状态"] != "待修改" {
		t.Fatalf("records=%+v", records)
	}
}

func TestResourceServiceRepliesToSubscribedCommentAsUser(t *testing.T) {
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		if req.Method != http.MethodPost || req.As != IdentityUser ||
			req.Path != "/open-apis/drive/v1/files/bas_bug/comments/comment_bug/replies" ||
			req.Params["file_type"] != "bitable" {
			t.Fatalf("request=%+v", req)
		}
		return map[string]any{"data": map[string]any{"reply_id": "reply_bug"}}
	}}
	result, err := NewResourceService(caller).ReplyToComment(
		context.Background(), "bas_bug", "bitable", "comment_bug", "已处理",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.ReplyID != "reply_bug" {
		t.Fatalf("result=%+v", result)
	}
}

func TestResourceServiceReadsCommentTextForCorrelation(t *testing.T) {
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		if req.Method != http.MethodGet || req.As != IdentityUser ||
			req.Path != "/open-apis/drive/v1/files/bas_bug/comments/comment_bug" {
			t.Fatalf("request=%+v", req)
		}
		return map[string]any{"data": map[string]any{
			"comment_id": "comment_bug", "user_id": "ou_reporter",
			"quote": "归档示例条目", "is_solved": false,
			"reply_list": map[string]any{"replies": []any{
				map[string]any{"content": map[string]any{"elements": []any{
					map[string]any{"text_run": map[string]any{"text": "BUG-99999 修复后请改状态"}},
				}}},
			}},
		}}
	}}
	comment, err := NewResourceService(caller).GetComment(
		context.Background(), "bas_bug", "bitable", "comment_bug",
	)
	if err != nil {
		t.Fatal(err)
	}
	if comment.ID != "comment_bug" || comment.Quote != "归档示例条目" ||
		comment.Text != "BUG-99999 修复后请改状态" {
		t.Fatalf("comment=%+v", comment)
	}
}

func TestResourceServiceComparesProjectedSingleSelectValues(t *testing.T) {
	gets := 0
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		switch req.Method {
		case http.MethodGet:
			gets++
			value := map[string]any{"name": "待修改"}
			if gets > 1 {
				value = map[string]any{"name": "已解决"}
			}
			return map[string]any{"data": map[string]any{"record": map[string]any{
				"record_id": "rec_bug", "fields": map[string]any{"状态": value},
			}}}
		case http.MethodPut:
			return map[string]any{"data": map[string]any{"record": map[string]any{"record_id": "rec_bug"}}}
		default:
			t.Fatalf("unexpected request=%+v", req)
			return nil
		}
	}}
	result, err := NewResourceService(caller).CompareAndUpdateBaseField(
		context.Background(),
		BaseFieldUpdate{
			AppToken: "bas_bug", TableID: "tbl_bug", RecordID: "rec_bug",
			FieldName: "状态", Before: "待修改", After: "已解决",
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Verified || gets != 2 {
		t.Fatalf("result=%+v gets=%d", result, gets)
	}
}

func TestResourceServiceRejectsStaleBaseStatusBeforeWrite(t *testing.T) {
	updates := 0
	caller := &resourceRoutingCaller{call: func(req APIRequest) any {
		if req.Method == http.MethodPut {
			updates++
		}
		return map[string]any{"data": map[string]any{"record": map[string]any{
			"record_id": "rec_bug", "fields": map[string]any{"状态": "验证中"},
		}}}
	}}
	_, err := NewResourceService(caller).CompareAndUpdateBaseField(
		context.Background(),
		BaseFieldUpdate{
			AppToken: "bas_bug", TableID: "tbl_bug", RecordID: "rec_bug",
			FieldName: "状态", Before: "待修改", After: "已解决",
		},
	)
	if err == nil || updates != 0 {
		t.Fatalf("err=%v updates=%d", err, updates)
	}
}
