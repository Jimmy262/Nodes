package main

import (
	assets "admin-servidores"
	"admin-servidores/internal/panel"
	"context"
	"crypto/rand"
	"encoding/hex"
	"flag"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"
)

func main() {
	port := flag.Int("port", 8080, "Puerto local")
	data := flag.String("data", "data/panel.db", "Archivo SQLite")
	resetPassword := flag.Bool("reset-password", false, "Restablecer el acceso local sin borrar servidores ni históricos")
	listenHost := flag.String("listen", "127.0.0.1", "Dirección de escucha; usa loopback detrás del proxy HTTPS")
	publicURL := flag.String("public-url", "", "Origen HTTPS público del panel")
	tlsCert := flag.String("tls-cert", "", "Certificado TLS opcional")
	tlsKey := flag.String("tls-key", "", "Clave TLS opcional")
	flag.Parse()
	if err := panel.ValidateSecretProtection(); err != nil {
		log.Fatal(err)
	}
	if (*tlsCert == "") != (*tlsKey == "") {
		log.Fatal("tls-cert y tls-key deben configurarse juntos")
	}
	ip := net.ParseIP(*listenHost)
	if ip == nil || (!ip.IsLoopback() && (*publicURL == "" || *tlsCert == "")) {
		log.Fatal("Para escuchar fuera de loopback configura public-url HTTPS y TLS, o utiliza un proxy HTTPS local")
	}
	token := make([]byte, 32)
	if _, err := rand.Read(token); err != nil {
		log.Fatal(err)
	}
	app, err := panel.New(*data, hex.EncodeToString(token))
	if err != nil {
		log.Fatal(err)
	}
	defer app.Close()
	if err := app.ConfigurePublicURL(*publicURL); err != nil {
		log.Fatal(err)
	}
	address := net.JoinHostPort(*listenHost, fmt.Sprint(*port))
	// Reserve the port before an explicit recovery can alter credentials.
	listener, err := net.Listen("tcp", address)
	if err != nil {
		log.Fatal(err)
	}
	defer listener.Close()
	if err := app.RecoverOperations(); err != nil {
		log.Fatal(err)
	}
	if *resetPassword {
		if err := app.ResetAuthentication(); err != nil {
			log.Fatal(err)
		}
	}
	configured, err := app.PasswordConfigured()
	if err != nil {
		log.Fatal(err)
	}
	files, err := fs.Sub(assets.Files, "web/dist")
	if err != nil {
		log.Fatal(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	app.StartCollection(ctx)
	srv := &http.Server{Addr: address, Handler: app.Handler(files), ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 2 * time.Minute, WriteTimeout: 3 * time.Minute, IdleTimeout: 60 * time.Second, MaxHeaderBytes: 64 << 10}
	origin := "http://" + address
	if *tlsCert != "" {
		origin = "https://" + address
	}
	if *publicURL != "" {
		origin = *publicURL
	}
	fmt.Printf("\nNodo: %s\n", origin)
	if configured {
		fmt.Println("Acceso con tu PIN. Las sesiones recordadas siguen activas tras reiniciar.")
	} else {
		fmt.Printf("Configura tu PIN una sola vez abriendo este enlace privado:\n%s/#setup=%s\n", origin, hex.EncodeToString(token))
	}
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		<-ctx.Done()
		c, done := context.WithTimeout(context.Background(), 5*time.Second)
		defer done()
		if err := srv.Shutdown(c); err != nil {
			srv.Close()
		}
	}()
	serve := srv.Serve
	if *tlsCert != "" {
		serve = func(l net.Listener) error { return srv.ServeTLS(l, *tlsCert, *tlsKey) }
	}
	if err := serve(listener); err != nil && err != http.ErrServerClosed {
		log.Print(err)
	}
	cancel()
	<-shutdownDone
}
