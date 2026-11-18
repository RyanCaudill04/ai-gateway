import './App.css'
import { TopBar } from './components/shell/TopBar.tsx'
import { ChatScreen } from './screens/ChatScreen.tsx'
import { KeysScreen } from './screens/KeysScreen.tsx'
import { NewScreen } from './screens/NewScreen.tsx'
import { RoutingScreen } from './screens/RoutingScreen.tsx'
import { useScreen } from './screens.ts'
import { useTheme } from './useTheme.ts'

function App() {
  const screen = useScreen()
  const [theme, toggleTheme] = useTheme()

  return (
    <div className="page">
      <TopBar screen={screen} theme={theme} onToggleTheme={toggleTheme} />
      {screen === 'new' && <NewScreen />}
      {screen === 'chats' && <ChatScreen />}
      {screen === 'routing' && <RoutingScreen />}
      {screen === 'keys' && <KeysScreen />}
    </div>
  )
}

export default App
