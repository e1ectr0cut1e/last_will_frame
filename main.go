package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Camera struct {
	Name string `yaml:"name"`
	URL  string `yaml:"url"`
}

type TelegramMessages struct {
	AccessDenied   string `yaml:"access_denied"`
	UnknownCommand string `yaml:"unknown_command"`
	VideoFailed    string `yaml:"video_failed"`
	SnapshotFailed string `yaml:"snapshot_failed"`
}

type TelegramCommand struct {
	Command     string `yaml:"command"`
	Type        string `yaml:"type"`
	Camera      string `yaml:"camera"`
	Description string `yaml:"description"`
}

type TelegramConfig struct {
	Token    string            `yaml:"token"`
	ChatId   int64             `yaml:"chat_id"`
	Messages TelegramMessages  `yaml:"messages"`
	Commands []TelegramCommand `yaml:"commands"`
}

type Config struct {
	SnapshotDir   string         `yaml:"snapshot_dir"`
	FFmpegBin     string         `yaml:"ffmpeg_bin"`
	VideoDuration int64          `yaml:"video_duration"`
	Cameras       []Camera       `yaml:"cameras"`
	Telegram      TelegramConfig `yaml:"telegram"`
}

var config = Config{}
var bot *tgbotapi.BotAPI

func Capture(streamName string, rtspUrl string) {
	m3u8Path := fmt.Sprintf("%s/%s.m3u8", config.SnapshotDir, streamName)
	tsDirPath := fmt.Sprintf("%s/%s", config.SnapshotDir, streamName)
	snapshotPath := fmt.Sprintf("%s/%s.jpg", config.SnapshotDir, streamName)
	_ = os.Remove(snapshotPath)
	_ = os.Remove(m3u8Path)
	_ = os.RemoveAll(tsDirPath)
	var initial = true
	for {
		prevSt, prevStErr := os.Stat(snapshotPath)
		prevMtime := time.Unix(0, 0)
		if prevStErr == nil {
			prevMtime = prevSt.ModTime()
		}
		lastMtime := time.Unix(0, 0)

		for range [3]struct{}{} {
			_ = os.Mkdir(tsDirPath, 0755)
			cmd := exec.Command(
				config.FFmpegBin,
				"-y", "-timeout", "1000000", "-re", "-rtsp_transport", "tcp", "-i", rtspUrl,
				"-an", "-c", "copy", "-f", "hls",
				"-hls_time", strconv.FormatInt(config.VideoDuration, 10),
				"-hls_list_size", "2",
				"-hls_flags", "delete_segments",
				"-strftime", "1",
				"-hls_segment_filename", fmt.Sprintf("%s/%%Y%%m%%d%%H%%M%%S.ts", tsDirPath),
				"-hls_base_url", fmt.Sprintf("%s/", streamName), m3u8Path,
				"-an", "-vf", "select='eq(pict_type,PICT_TYPE_I)'",
				"-vsync", "vfr", "-q:v", "23", "-update", "1", snapshotPath,
			)
			_ = cmd.Run()
			lastSt, lastStErr := os.Stat(snapshotPath)
			if lastStErr == nil {
				lastMtime = lastSt.ModTime()
			}
			if prevMtime != lastMtime || initial {
				log.Println(fmt.Sprintf("FFmpeg for %s has failed", streamName))
			}
		}

		if prevMtime != lastMtime {
			_, err := os.Stat(m3u8Path)
			if err == nil {
				SendVideo(streamName)
				_ = os.Remove(m3u8Path)
				_ = os.RemoveAll(tsDirPath)
			} else {
				SendSnap(streamName)
			}
		}
		initial = false
	}
}

func SendSnap(streamName string) {
	snapshotPath := fmt.Sprintf("%s/%s.jpg", config.SnapshotDir, streamName)
	if _, err := os.Stat(snapshotPath); err == nil {
		photoFileBytes := tgbotapi.FilePath(snapshotPath)
		msg := tgbotapi.NewPhoto(config.Telegram.ChatId, photoFileBytes)
		if _, err := bot.Send(msg); err != nil {
			log.Println("Failed to send snapshot:", err)
		}
	} else {
		msg := tgbotapi.NewMessage(config.Telegram.ChatId, config.Telegram.Messages.SnapshotFailed)
		if _, err := bot.Send(msg); err != nil {
			log.Println("Failed to send message:", err)
		}
	}
}

func SendVideo(streamName string) {
	m3u8Path := fmt.Sprintf("%s/%s.m3u8", config.SnapshotDir, streamName)
	mp4Path := fmt.Sprintf("%s/%s.mp4", config.SnapshotDir, streamName)

	live := true
	f, err := os.OpenFile(m3u8Path, os.O_RDONLY, os.ModePerm)
	if err != nil {
		log.Println(fmt.Sprintf("Error opening %s:", m3u8Path), err)
	} else {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "#EXT-X-ENDLIST") {
				live = false
			}
		}
		if err := sc.Err(); err != nil {
			log.Println(fmt.Sprintf("Error reading %s:", m3u8Path), err)
		}
	}
	_ = f.Close()
	args := []string{"-y"}
	if live {
		args = append(
			args,
			"-sseof", fmt.Sprintf("-%d", config.VideoDuration),
			"-t", strconv.FormatInt(config.VideoDuration, 10),
		)
	}
	args = append(args, "-i", m3u8Path, "-ignore_unknown", "-fflags", "+igndts", "-c", "copy", mp4Path)

	cmd := exec.Command(config.FFmpegBin, args...)
	log.Println("Running FFmpeg:", cmd.String())
	_ = cmd.Run()
	if _, err := os.Stat(mp4Path); err == nil {
		videoFileBytes := tgbotapi.FilePath(mp4Path)
		msg := tgbotapi.NewVideo(config.Telegram.ChatId, videoFileBytes)
		if _, err := bot.Send(msg); err != nil {
			log.Println("Failed to send video:", err)
		}
	} else {
		msg := tgbotapi.NewMessage(config.Telegram.ChatId, config.Telegram.Messages.VideoFailed)
		if _, err := bot.Send(msg); err != nil {
			log.Println("Failed to send message:", err)
		}
	}
	_ = os.Remove(mp4Path)
}

func main() {
	var err error
	if len(os.Args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, fmt.Sprintf("Usage: %s <CONFIG.YAML>", os.Args[0]))
		os.Exit(1)
	}
	f, err := os.OpenFile(os.Args[1], os.O_RDONLY, os.ModePerm)
	if err != nil {
		log.Fatalln("Failed to open the configuration file:", err)
	}
	decoder := yaml.NewDecoder(f)
	err = decoder.Decode(&config)
	_ = f.Close()
	if err != nil {
		log.Fatalln("Failed to decode configuration:", err)
	}
	err = os.MkdirAll(config.SnapshotDir, 0o0755)
	if err != nil {
		log.Fatalln("Failed to create snapshot directory:", err)
	}
	for _, camera := range config.Cameras {
		go Capture(
			camera.Name,
			camera.URL,
		)
	}
	bot, err = tgbotapi.NewBotAPI(config.Telegram.Token)
	if err != nil {
		log.Fatalln("Failed to initialize Telegram:", err)
	}

	log.Println("Authorized on account:", bot.Self.UserName)
	var commands []tgbotapi.BotCommand
	for _, command := range config.Telegram.Commands {
		commands = append(commands, tgbotapi.BotCommand{
			Command:     command.Command,
			Description: command.Description,
		})
	}

	scope := tgbotapi.NewBotCommandScopeChat(config.Telegram.ChatId)
	commandsConfig := tgbotapi.NewSetMyCommandsWithScope(scope, commands...)
	if _, err = bot.Request(commandsConfig); err != nil {
		log.Println("Failed to set Telegram commands:", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message != nil {
			log.Println(
				fmt.Sprintf("Got message from chat %d from %s", update.Message.Chat.ID, update.Message.From),
			)

			if update.Message.Chat.ID != config.Telegram.ChatId {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, config.Telegram.Messages.AccessDenied)
				if _, err := bot.Send(msg); err != nil {
					log.Println("Failed to send message:", err)
				}
				continue
			}

			if !update.Message.IsCommand() {
				continue
			}

			processed := false
			for _, command := range config.Telegram.Commands {
				if update.Message.Command() == command.Command {
					if command.Type == "snap" {
						SendSnap(command.Camera)
						processed = true
						break
					} else if command.Type == "video" {
						SendVideo(command.Camera)
						processed = true
						break
					}
				}
			}

			if !processed {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, config.Telegram.Messages.UnknownCommand)
				if _, err := bot.Send(msg); err != nil {
					log.Println("Failed to send message:", err)
				}
			}
		}
	}
}
