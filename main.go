package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"

	"github.com/urfave/cli/v3"
)

func main() {
	os.Exit(runCLI(os.Args))
}

type commandDependencies struct {
	runServer   func(context.Context) error
	runBackfill func(context.Context, backfillCLIConfig, io.Writer) error
}

func runCLI(args []string) int {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	slog.SetDefault(logger)
	ignoreSIGPIPE()

	ctx, stop := notifyContextTwoStage(context.Background(), func() { os.Exit(130) }, syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	cmd := newCommand(commandDependencies{
		runServer:   runServer,
		runBackfill: runBackfill,
	})
	if err := cmd.Run(ctx, args); err != nil {
		slog.Error("命令执行失败", "error", err)
		return commandExitCode(err)
	}
	return 0
}

// ignoreSIGPIPE 让写断开的 stdout/stderr 返回 EPIPE，而不是杀死进程。
// Ctrl+C 打的是整个前台进程组，管道下游（如 tee）默认处置就是立刻死，
// 下游一死 stdout 即断开；此时收尾阶段的第一条日志会让进程死于 SIGPIPE，
// 中断后当前条目可能无法写完。日志写不出去可以接受，收尾丢失不行。
func ignoreSIGPIPE() {
	signal.Ignore(syscall.SIGPIPE)
}

// notifyContextTwoStage 首个信号取消返回的 ctx，第二个信号调用 onSecond。
// signal.NotifyContext 在首个信号后仍占着 SIGINT 的处理权且不再消费，
// 中断后的收尾工作可能要跑几分钟，必须留一个立即退出的逃生口。
func notifyContextTwoStage(parent context.Context, onSecond func(), signals ...os.Signal) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	signalsCh := make(chan os.Signal, 2)
	signal.Notify(signalsCh, signals...)
	done := make(chan struct{})
	var once sync.Once
	stop := func() {
		once.Do(func() {
			signal.Stop(signalsCh)
			close(done)
			cancel()
		})
	}
	go func() {
		defer stop()
		select {
		case <-signalsCh:
			cancel()
			// 文案不能提回填：这里同时罩着 server 和 backfill 两条子命令。
			slog.Warn("收到中断信号：正在收尾，再按一次 Ctrl+C 立即退出")
		case <-ctx.Done():
			return
		}
		select {
		case <-signalsCh:
			onSecond()
		case <-done:
		case <-parent.Done():
		}
	}()
	return ctx, stop
}

func commandExitCode(err error) int {
	var exitCoder cli.ExitCoder
	if errors.As(err, &exitCoder) {
		return exitCoder.ExitCode()
	}
	return 1
}

// helpSectionTitles 把 urfave/cli 帮助模板里的英文段落标题换成中文。
// 用替换而不是整段复制模板：复制会让副本在升级 cli 时悄悄落后于上游的模板改动，
// 替换只动标题词，其余模板逻辑继续跟随上游。"GLOBAL OPTIONS:" 必须先于 "OPTIONS:" 列出。
var helpSectionTitles = strings.NewReplacer(
	"GLOBAL OPTIONS:", "全局选项：",
	"OPTIONS:", "选项：",
	"NAME:", "名称：",
	"USAGE:", "用法：",
	"VERSION:", "版本：",
	"DESCRIPTION:", "说明：",
	"COMMANDS:", "命令：",
	"CATEGORY:", "分类：",
	"COPYRIGHT:", "版权：",
)

// localizeHelp 汉化 urfave/cli 自带的帮助文案。替换是幂等的，重复调用无副作用。
func localizeHelp() {
	// HideDefault 和 Local 沿用上游默认值，否则会多出 "(default: false)" 并传播到子命令。
	cli.HelpFlag = &cli.BoolFlag{
		Name:        "help",
		Aliases:     []string{"h"},
		Usage:       "显示帮助",
		HideDefault: true,
		Local:       true,
	}
	cli.UsageCommandHelp = "列出全部命令，或查看某个命令的帮助"
	cli.ArgsUsageCommandHelp = "[命令]"
	cli.RootCommandHelpTemplate = helpSectionTitles.Replace(cli.RootCommandHelpTemplate)
	cli.CommandHelpTemplate = helpSectionTitles.Replace(cli.CommandHelpTemplate)
	cli.SubcommandHelpTemplate = helpSectionTitles.Replace(cli.SubcommandHelpTemplate)
}

func newCommand(deps commandDependencies) *cli.Command {
	localizeHelp()
	return &cli.Command{
		Name:      "rssgen",
		Usage:     "将网站内容转换为 RSS/Atom，并提供历史回填工具",
		UsageText: "rssgen [全局选项] [命令 [命令选项]]",
		Suggest:   true,
		Commands: []*cli.Command{
			{
				Name:      "server",
				Usage:     "启动 RSS/Atom HTTP 服务",
				UsageText: "rssgen server [选项]",
				Action: func(ctx context.Context, cmd *cli.Command) error {
					if err := rejectPositionalArgs(cmd); err != nil {
						return err
					}
					return deps.runServer(ctx)
				},
			},
			newBackfillCommand(deps),
		},
		// cli.Exit 默认会直接调用 os.Exit；交给 main 统一记录错误并决定退出码。
		ExitErrHandler: func(context.Context, *cli.Command, error) {},
	}
}

func rejectPositionalArgs(cmd *cli.Command) error {
	if cmd.NArg() == 0 {
		return nil
	}
	return fmt.Errorf("不接受位置参数: %s", strings.Join(cmd.Args().Slice(), " "))
}
