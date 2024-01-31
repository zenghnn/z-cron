# cron定时任务管理器
* 支持cron表达式
* 支持L,W和年份
* 表达式解析包: <https://github.com/gorhill/cronexpr>
* 任务管理器: <https://github.com/robfig/cron> (重写)
* 增加了一些功能 RemoveAll,删除所有任务
* 传入6位表达式会自动转为5位置,即去掉秒位置

License
-------
License: pick the one which suits you best:
- GPL v3 see https://www.gnu.org/licenses/gpl.html
- APL v2 see http://www.apache.org/licenses/LICENSE-2.0