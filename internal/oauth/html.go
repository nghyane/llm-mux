// Package oauth provides OAuth HTML response generation.
// This file provides the public API for generating OAuth callback HTML pages.
// Templates are embedded and parsed once at startup for performance.
// Fallback HTML is provided for error resilience.
package oauth

import (
	"fmt"
	"html"

	log "github.com/sirupsen/logrus"
)

// HTMLSuccess returns a success page for CLI mode.
// Uses embedded templates with fallback to inline HTML on error.
func HTMLSuccess() string {
	result, err := RenderSuccess()
	if err != nil {
		log.WithError(err).Warn("Failed to render success template, using fallback")
		return fallbackHTMLSuccess
	}
	return result
}

// HTMLError returns an error page for CLI mode.
// Uses embedded templates with fallback to inline HTML on error.
func HTMLError(message string) string {
	result, err := RenderError(message)
	if err != nil {
		log.WithError(err).Warn("Failed to render error template, using fallback")
		return fallbackHTMLError(message)
	}
	return result
}

// HTMLSuccessWithPostMessage returns a success page that notifies parent window via postMessage.
// This is used for Web UI mode where React FE needs to know when OAuth completes.
func HTMLSuccessWithPostMessage(provider, state string) string {
	result, err := RenderSuccessWebUI(provider, state)
	if err != nil {
		log.WithError(err).Warn("Failed to render webui success template, using fallback")
		return fallbackHTMLSuccessWithPostMessage(provider, state)
	}
	return result
}

// HTMLErrorWithPostMessage returns an error page that notifies parent window via postMessage.
// This is used for Web UI mode where React FE needs to know when OAuth fails.
func HTMLErrorWithPostMessage(provider, state, message string) string {
	result, err := RenderErrorWebUI(provider, state, message)
	if err != nil {
		log.WithError(err).Warn("Failed to render webui error template, using fallback")
		return fallbackHTMLErrorWithPostMessage(provider, state, message)
	}
	return result
}

// Fallback HTML templates for resilience.
// These are used if embedded templates fail to load/render.

const fallbackHTMLSuccess = `<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Successful</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
               display: flex; justify-content: center; align-items: center; height: 100vh;
               margin: 0; background: linear-gradient(135deg, #667eea 0%, #764ba2 100%); }
        .container { text-align: center; background: white; padding: 40px 60px;
                     border-radius: 16px; box-shadow: 0 10px 40px rgba(0,0,0,0.2); }
        h1 { color: #22c55e; margin-bottom: 16px; }
        p { color: #666; margin: 8px 0; }
        .icon { font-size: 64px; margin-bottom: 16px; }
    </style>
    <script>setTimeout(function(){ window.close(); }, 5000);</script>
</head>
<body>
    <div class="container">
        <div class="icon">&#10003;</div>
        <h1>Authentication Successful!</h1>
        <p>You can close this window.</p>
        <p style="color: #999; font-size: 14px;">This window will close automatically in 5 seconds.</p>
    </div>
</body>
</html>`

func fallbackHTMLError(message string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Failed</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
               display: flex; justify-content: center; align-items: center; height: 100vh;
               margin: 0; background: linear-gradient(135deg, #ff6b6b 0%%, #ee5a5a 100%%); }
        .container { text-align: center; background: white; padding: 40px 60px;
                     border-radius: 16px; box-shadow: 0 10px 40px rgba(0,0,0,0.2); }
        h1 { color: #ef4444; margin-bottom: 16px; }
        p { color: #666; margin: 8px 0; }
        .error { background: #fef2f2; color: #b91c1c; padding: 12px 20px;
                 border-radius: 8px; margin-top: 16px; font-size: 14px; }
        .icon { font-size: 64px; margin-bottom: 16px; }
    </style>
</head>
<body>
    <div class="container">
        <div class="icon">&#10007;</div>
        <h1>Authentication Failed</h1>
        <p>An error occurred during authentication.</p>
        <div class="error">%s</div>
        <p style="color: #999; font-size: 14px; margin-top: 20px;">Please close this window and try again.</p>
    </div>
</body>
</html>`, html.EscapeString(message))
}

func fallbackHTMLSuccessWithPostMessage(provider, state string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Successful</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
               display: flex; justify-content: center; align-items: center; height: 100vh;
               margin: 0; background: linear-gradient(135deg, #667eea 0%%, #764ba2 100%%); }
        .container { text-align: center; background: white; padding: 40px 60px;
                     border-radius: 16px; box-shadow: 0 10px 40px rgba(0,0,0,0.2); }
        h1 { color: #22c55e; margin-bottom: 16px; }
        p { color: #666; margin: 8px 0; }
        .icon { font-size: 64px; margin-bottom: 16px; }
    </style>
    <script>
    (function() {
        var result = {
            type: 'oauth-callback',
            provider: %q,
            state: %q,
            status: 'success',
            error: ''
        };
        if (window.opener) window.opener.postMessage(result, '*');
        if (window.parent && window.parent !== window) window.parent.postMessage(result, '*');
        setTimeout(function() { window.close(); }, 2000);
    })();
    </script>
</head>
<body>
    <div class="container">
        <div class="icon">&#10003;</div>
        <h1>Authentication Successful!</h1>
        <p>Redirecting back to application...</p>
        <p style="color: #999; font-size: 14px;">This window will close automatically.</p>
    </div>
</body>
</html>`, provider, state)
}

func fallbackHTMLErrorWithPostMessage(provider, state, message string) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html lang="en">
<head>
    <meta charset="utf-8">
    <meta name="viewport" content="width=device-width, initial-scale=1.0">
    <title>Authentication Failed</title>
    <style>
        body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif;
               display: flex; justify-content: center; align-items: center; height: 100vh;
               margin: 0; background: linear-gradient(135deg, #ff6b6b 0%%, #ee5a5a 100%%); }
        .container { text-align: center; background: white; padding: 40px 60px;
                     border-radius: 16px; box-shadow: 0 10px 40px rgba(0,0,0,0.2); }
        h1 { color: #ef4444; margin-bottom: 16px; }
        p { color: #666; margin: 8px 0; }
        .error { background: #fef2f2; color: #b91c1c; padding: 12px 20px;
                 border-radius: 8px; margin-top: 16px; font-size: 14px; }
        .icon { font-size: 64px; margin-bottom: 16px; }
    </style>
    <script>
    (function() {
        var result = {
            type: 'oauth-callback',
            provider: %q,
            state: %q,
            status: 'error',
            error: %q
        };
        if (window.opener) window.opener.postMessage(result, '*');
        if (window.parent && window.parent !== window) window.parent.postMessage(result, '*');
        setTimeout(function() { window.close(); }, 5000);
    })();
    </script>
</head>
<body>
    <div class="container">
        <div class="icon">&#10007;</div>
        <h1>Authentication Failed</h1>
        <p>An error occurred during authentication.</p>
        <div class="error">%s</div>
        <p style="color: #999; font-size: 14px; margin-top: 20px;">This window will close automatically.</p>
    </div>
</body>
</html>`, provider, state, message, html.EscapeString(message))
}
