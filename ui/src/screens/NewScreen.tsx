import { ScreenLayout } from '../components/shell/ScreenLayout.tsx'

/** Placeholder until this screen's own PR lands (tracking: #163). */
export function NewScreen() {
  return (
    <ScreenLayout
      sideLabel="New conversation"
      side={null}
      main={<h1 style={{ padding: 24, fontSize: 17 }}>New conversation</h1>}
      inspectorLabel="Details"
      inspector={null}
    />
  )
}
