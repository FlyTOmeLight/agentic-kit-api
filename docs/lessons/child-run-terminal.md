# 父子 Run 锁序与结果归属

父 Run 取消时需要锁住活动子 Run，子 Run 完成时也要更新父 Run。两条路径都先锁父行，再锁子行，然后处理 Step。需要检查 Goal 的派生事务先锁 Goal。领取事务可能先用 `SKIP LOCKED` 选中子 Run，发现它过期或耗尽尝试次数时释放该事务，再按父子顺序加锁，重新确认子 Run 仍可落失败。 [终态收口](../../pkg/durable/subtask.go) [领取失败路径](../../pkg/durable/lease.go)

父 Run 等待的 Step 和父子两处 `child:` 引用一致时，终态收口才保存结果并重新排队。匹配的 Step 已保存相同结果时直接返回。取消级联只对成功从活动态转为取消的子 Run 写事件。 [终态、并发取消和回调隔离用例](../../pkg/durable/child_run_test.go)
