package proxy

import (
	"context"
	"fmt"
	"io"
	"net"
	"sync"
	"time"
)

const defaultBufferSize = 32 * 1024

var bufferPool = sync.Pool{
	New: func() interface{} {
		b := make([]byte, defaultBufferSize)
		return &b
	},
}

// Run acepta conexiones en listenAddr, las limita con tryAccept (si no nil) y las reenvía a backendAddr.
// tryAccept(ip string) (allow bool, reason string). Si allow es false, se rechaza y reason se usa para logs.
// onAccept(ip, reason) se llama al aceptar; onReject(ip, reason) al rechazar; onRelease(ip) al cerrar.
// idleTimeout 0 = sin timeout.
// shouldDrain es una función que retorna true si el listener debe entrar en modo drain (cerrar temporalmente).
// Si shouldDrain es nil, nunca entrará en modo drain.
func Run(ctx context.Context, listenAddr, backendAddr string, idleTimeout time.Duration,
	tryAccept func(ip string) (allow bool, reason string),
	onAccept func(ip string), onReject func(ip, reason string), onRelease func(ip string),
	shouldDrain func() bool,
) error {
	var ln net.Listener
	var err error
	var mu sync.Mutex

	// Función para crear/cerrar listener
	createListener := func() (net.Listener, error) {
		return net.Listen("tcp", listenAddr)
	}

	closeListener := func() {
		mu.Lock()
		if ln != nil {
			_ = ln.Close()
			ln = nil
		}
		mu.Unlock()
	}

	// Crear listener inicial
	ln, err = createListener()
	if err != nil {
		return fmt.Errorf("no se pudo crear listener en %s: %w", listenAddr, err)
	}

	go func() {
		<-ctx.Done()
		closeListener()
	}()

	// Contador de rechazos recientes para backoff adaptativo
	var (
		rejectCountMu   sync.Mutex
		rejectCount     int
		lastRejectReset = time.Now()
	)

	// Función para obtener delay basado en rechazos recientes
	getBackoffDelay := func() time.Duration {
		rejectCountMu.Lock()
		defer rejectCountMu.Unlock()

		// Resetear contador cada segundo
		if time.Since(lastRejectReset) >= time.Second {
			rejectCount = 0
			lastRejectReset = time.Now()
		}

		// Backoff exponencial basado en rechazos
		if rejectCount > 100 {
			return 100 * time.Millisecond // 100ms si hay muchos rechazos
		} else if rejectCount > 50 {
			return 50 * time.Millisecond // 50ms
		} else if rejectCount > 20 {
			return 10 * time.Millisecond // 10ms
		}
		return 0 // Sin delay si hay pocos rechazos
	}

	// Función para incrementar contador de rechazos
	incrementRejectCount := func() {
		rejectCountMu.Lock()
		rejectCount++
		rejectCountMu.Unlock()
	}

	for {
		// Verificar si debemos entrar en modo drain
		if shouldDrain != nil && shouldDrain() {
			// Cerrar listener temporalmente (modo drain)
			mu.Lock()
			if ln != nil {
				_ = ln.Close()
				ln = nil
			}
			mu.Unlock()

			// Resetear contador de rechazos en modo drain
			rejectCountMu.Lock()
			rejectCount = 0
			rejectCountMu.Unlock()

			// Esperar un poco antes de intentar reabrir (más rápido para recuperación rápida)
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(2 * time.Second):
				// Intentar reabrir si ya no está en drain
				if shouldDrain == nil || !shouldDrain() {
					newLn, err := createListener()
					if err != nil {
						// Si falla, esperar y reintentar
						select {
						case <-ctx.Done():
							return nil
						case <-time.After(2 * time.Second):
							continue
						}
					}
					mu.Lock()
					ln = newLn
					mu.Unlock()
				}
			}
			continue
		}

		// Backoff adaptativo para reducir CPU cuando hay muchos rechazos
		if delay := getBackoffDelay(); delay > 0 {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(delay):
				// Continuar después del delay
			}
		}

		// Asegurar que el listener esté abierto
		mu.Lock()
		if ln == nil {
			newLn, err := createListener()
			if err != nil {
				mu.Unlock()
				// Si falla, esperar y reintentar
				select {
				case <-ctx.Done():
					return nil
				case <-time.After(2 * time.Second):
					continue
				}
			}
			ln = newLn
		}
		currentLn := ln
		mu.Unlock()

		client, err := currentLn.Accept()
		if err != nil {
			mu.Lock()
			// Si el listener fue cerrado, intentar recrearlo
			if ln == nil {
				mu.Unlock()
				continue
			}
			mu.Unlock()

			if ctx.Err() != nil {
				// Contexto cancelado, retornar sin error
				return nil
			}
			// Error inesperado del listener - reintentar con delay
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(100 * time.Millisecond):
				continue
			}
		}

		// Procesar conexión en goroutine
		go func(c net.Conn) {
			defer func() {
				if r := recover(); r != nil {
					// evitar crash global
				}
			}()
			// Verificar si fue rechazada para incrementar contador
			wasRejected := false
			originalOnReject := onReject
			wrappedOnReject := func(ip, reason string) {
				wasRejected = true
				incrementRejectCount()
				originalOnReject(ip, reason)
			}
			handleConn(ctx, c, backendAddr, idleTimeout, tryAccept, onAccept, wrappedOnReject, onRelease)
			// Si no fue rechazada, resetear contador parcialmente
			if !wasRejected {
				rejectCountMu.Lock()
				if rejectCount > 0 {
					rejectCount--
				}
				rejectCountMu.Unlock()
			}
		}(client)
	}
}

func handleConn(ctx context.Context, client net.Conn, backendAddr string, idleTimeout time.Duration,
	tryAccept func(ip string) (allow bool, reason string),
	onAccept func(ip string), onReject func(ip, reason string), onRelease func(ip string),
) {
	defer client.Close()
	ip := remoteIP(client)
	if tryAccept != nil {
		allow, reason := tryAccept(ip)
		if !allow {
			onReject(ip, reason)
			return
		}
		defer onRelease(ip)
		onAccept(ip)
	}

	backend, err := net.DialTimeout("tcp", backendAddr, 10*time.Second)
	if err != nil {
		onReject(ip, "backend_fail")
		return
	}
	defer backend.Close()

	if tcp, ok := client.(*net.TCPConn); ok {
		tcp.SetKeepAlive(true)
	}
	if tcp, ok := backend.(*net.TCPConn); ok {
		tcp.SetKeepAlive(true)
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Cerrar ambos al cerrar cualquiera
	go func() {
		<-ctx.Done()
		_ = client.Close()
		_ = backend.Close()
	}()

	// NO establecemos deadline inicial aquí. Confiamos en:
	// 1. TCP keep-alive (ya habilitado arriba) para detectar conexiones realmente muertas
	// 2. El timeout en deadlineConn solo para operaciones de I/O activas (Read/Write)
	// Esto permite que los usuarios se queden quietos sin perder la conexión,
	// mientras que TCP keep-alive detecta conexiones muertas automáticamente.

	// Copia bidireccional con io.CopyBuffer y buffers del pool (uno por dirección)
	buf1 := bufferPool.Get().(*[]byte)
	buf2 := bufferPool.Get().(*[]byte)
	defer bufferPool.Put(buf1)
	defer bufferPool.Put(buf2)
	done := make(chan struct{}, 2)
	srcClient := &deadlineConn{Conn: client, timeout: idleTimeout}
	srcBackend := &deadlineConn{Conn: backend, timeout: idleTimeout}
	go func() {
		defer func() { done <- struct{}{} }()
		io.CopyBuffer(srcBackend, srcClient, *buf1)
		_ = backend.Close()
	}()
	go func() {
		defer func() { done <- struct{}{} }()
		io.CopyBuffer(srcClient, srcBackend, *buf2)
		_ = client.Close()
	}()
	<-done
	cancel()
	<-done
}

// deadlineConn aplica timeout solo a operaciones de I/O activas (Read/Write).
// Esto significa que si no hay tráfico, no se fuerza el cierre de la conexión.
// TCP keep-alive se encarga de detectar conexiones realmente muertas.
// El timeout aquí es para evitar que operaciones de I/O se queden colgadas indefinidamente.
type deadlineConn struct {
	net.Conn
	timeout time.Duration
}

func (c *deadlineConn) Read(b []byte) (n int, err error) {
	if c.timeout > 0 {
		c.Conn.SetReadDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Read(b)
}

func (c *deadlineConn) Write(b []byte) (n int, err error) {
	if c.timeout > 0 {
		c.Conn.SetWriteDeadline(time.Now().Add(c.timeout))
	}
	return c.Conn.Write(b)
}

func remoteIP(conn net.Conn) string {
	if addr := conn.RemoteAddr(); addr != nil {
		if t, ok := addr.(*net.TCPAddr); ok {
			return t.IP.String()
		}
		return addr.String()
	}
	return ""
}
