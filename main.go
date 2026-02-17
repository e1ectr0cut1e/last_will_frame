package main

import (
	"bufio"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func getEnvDefault(key, fallback string) string {
	if value, ok := os.LookupEnv(key); ok {
		return value
	}
	return fallback
}

var telegramToken = os.Getenv("TELEGRAM_TOKEN")
var chatId = os.Getenv("CHAT_ID")
var cameraAddresses = os.Getenv("RTSP_ADDRESSES")
var rtspUsername = os.Getenv("RTSP_USERNAME")
var rtspPassword = os.Getenv("RTSP_PASSWORD")
var rtspUrl = getEnvDefault("RTSP_URL", "/")
var ffmpegBin = getEnvDefault("FFMPEG_BIN", "/usr/bin/ffmpeg")
var snapshotDir = getEnvDefault("SNAPSHOT_DIR", "/dev/shm/last_will_frame_snapshots")
var videoDuration = getEnvDefault("VIDEO_DURATION", "10")

var numericChatId, _ = strconv.ParseInt(chatId, 10, 64)

var bot *tgbotapi.BotAPI

func Capture(rtspUrl string, streamName string) {
	m3u8Path := fmt.Sprintf("%s/%s.m3u8", snapshotDir, streamName)
	tsDirPath := fmt.Sprintf("%s/%s", snapshotDir, streamName)
	snapshotPath := fmt.Sprintf("%s/%s.jpg", snapshotDir, streamName)
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
				ffmpegBin,
				"-y", "-timeout", "1000000", "-re", "-rtsp_transport", "tcp", "-i",
				rtspUrl, "-an", "-c", "copy", "-f", "hls", "-hls_time", videoDuration, "-hls_list_size", "2",
				"-hls_flags", "delete_segments", "-strftime", "1",
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
	snapshotPath := fmt.Sprintf("%s/%s.jpg", snapshotDir, streamName)
	if _, err := os.Stat(snapshotPath); err == nil {
		photoFileBytes := tgbotapi.FilePath(snapshotPath)
		msg := tgbotapi.NewPhoto(numericChatId, photoFileBytes)
		if _, err := bot.Send(msg); err != nil {
			log.Println(err)
		}
	} else {
		msg := tgbotapi.NewMessage(numericChatId, "🚫 Snapshot doesn't exist")
		if _, err := bot.Send(msg); err != nil {
			log.Println(err)
		}
	}
}

func SendVideo(streamName string) {
	m3u8Path := fmt.Sprintf("%s/%s.m3u8", snapshotDir, streamName)
	mp4Path := fmt.Sprintf("%s/%s.mp4", snapshotDir, streamName)

	live := true
	f, err := os.OpenFile(m3u8Path, os.O_RDONLY, os.ModePerm)
	if err != nil {
		log.Println(fmt.Sprintf("Error opening %s: %v", m3u8Path, err))
	} else {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if strings.Contains(sc.Text(), "#EXT-X-ENDLIST") {
				live = false
			}
		}
		if err := sc.Err(); err != nil {
			log.Println(fmt.Sprintf("Error reading %s: %v", m3u8Path, err))
		}
	}
	_ = f.Close()
	args := []string{"-y"}
	if live {
		args = append(args, "-sseof", "-"+videoDuration, "-t", videoDuration)
	}
	args = append(args, "-i", m3u8Path, "-ignore_unknown", "-fflags", "+igndts", "-c", "copy", mp4Path)

	cmd := exec.Command(ffmpegBin, args...)
	log.Println(cmd.String())
	_ = cmd.Run()
	if _, err := os.Stat(mp4Path); err == nil {
		videoFileBytes := tgbotapi.FilePath(mp4Path)
		msg := tgbotapi.NewVideo(numericChatId, videoFileBytes)
		if _, err := bot.Send(msg); err != nil {
			log.Println(err)
		}
	} else {
		msg := tgbotapi.NewMessage(numericChatId, "🚫 Failed to create video")
		if _, err := bot.Send(msg); err != nil {
			log.Println(err)
		}
	}
	_ = os.Remove(mp4Path)
}

func main() {
	var err error
	err = os.MkdirAll(snapshotDir, 0o0755)
	if err != nil {
		log.Fatal(err)
	}
	addresses := strings.Split(cameraAddresses, ",")
	for i := 0; i < len(addresses); i++ {
		streamName := fmt.Sprintf("snap%d", i)
		go Capture(
			fmt.Sprintf("rtsp://%s:%s@%s%s", rtspUsername, rtspPassword, addresses[i], rtspUrl),
			streamName,
		)
	}
	bot, err = tgbotapi.NewBotAPI(telegramToken)
	if err != nil {
		log.Panic(err)
	}

	log.Println("Authorized on account:", bot.Self.UserName)
	var commands []tgbotapi.BotCommand
	for i := range strings.Split(cameraAddresses, ",") {
		commands = append(commands, tgbotapi.BotCommand{
			Command:     fmt.Sprintf("/snap%d", i),
			Description: fmt.Sprintf("📷 get a snapshot for channel %d", i),
		})
	}
	for i := range strings.Split(cameraAddresses, ",") {
		commands = append(commands, tgbotapi.BotCommand{
			Command:     fmt.Sprintf("/vid%d", i),
			Description: fmt.Sprintf("📹 get a %s-second video for channel %d", videoDuration, i),
		})
	}

	scope := tgbotapi.NewBotCommandScopeChat(numericChatId)
	commandsConfig := tgbotapi.NewSetMyCommandsWithScope(scope, commands...)
	if _, err = bot.Request(commandsConfig); err != nil {
		log.Println("Unable to set Telegram commands", err)
	}

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := bot.GetUpdatesChan(u)
	for update := range updates {
		if update.Message != nil {
			log.Println(
				fmt.Sprintf("Got message from chat %d from %s", update.Message.Chat.ID, update.Message.From),
			)

			if update.Message.Chat.ID != numericChatId {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "🚫 Access denied")
				if _, err := bot.Send(msg); err != nil {
					log.Println(err)
				}
				continue
			}

			if !update.Message.IsCommand() {
				continue
			}

			if match, err := regexp.MatchString("^snap\\d$", update.Message.Command()); err == nil && match {
				snapNum := strings.TrimPrefix(update.Message.Command(), "snap")
				SendSnap("snap" + snapNum)
			} else if match, err := regexp.MatchString("^vid\\d$", update.Message.Command()); err == nil && match {
				snapNum := strings.TrimPrefix(update.Message.Command(), "vid")
				SendVideo("snap" + snapNum)
			} else {
				msg := tgbotapi.NewMessage(update.Message.Chat.ID, "🚫 Unsupported command")
				if _, err := bot.Send(msg); err != nil {
					log.Println(err)
				}
			}
		}
	}
}
